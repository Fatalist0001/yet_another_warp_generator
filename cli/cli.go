// Package cli — CLI-интерфейс warpgen.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alexgen/warpgen/config"
	"github.com/alexgen/warpgen/dnscheck"
	"github.com/alexgen/warpgen/registration"
	"github.com/alexgen/warpgen/scanner"
	"github.com/alexgen/warpgen/tunnel"
)

// Version подставляется из main (ldflags) — для команды `version`.
var Version = "dev"

// Run — основная точка входа CLI.
func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return errors.New("нет команды")
	}
	command := strings.TrimSpace(args[0])
	switch command {
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "version", "-v", "--version":
		fmt.Printf("warpgen %s\n", Version)
		return nil
	case "register":
		return runRegister(args[1:])
	case "scan":
		return runScan(args[1:])
	case "dns-check":
		return runDNSCheck(args[1:])
	case "gen":
		return runGen(args[1:])
	case "chain":
		return runChain(args[1:])
	default:
		return fmt.Errorf("неизвестная команда: %s (используйте help)", command)
	}
}

// commonFlags — флаги, общие для всех команд.
type commonFlags struct {
	account *string
	fresh   *bool
	iface   *string
	debug   *bool
	json    *bool
}

func addCommon(fs *flag.FlagSet) *commonFlags {
	return &commonFlags{
		account: fs.String("account", registration.DefaultAccountFile, "файл аккаунта WARP"),
		fresh:   fs.Bool("fresh", false, "зарегистрировать новый аккаунт даже при наличии файла"),
		iface:   fs.String("interface", "", "привязать UDP-сокет к интерфейсу (linux, нужен root)"),
		debug:   fs.Bool("debug", false, "подробный вывод"),
		json:    fs.Bool("json", false, "вывод в JSON"),
	}
}

// --- register ---

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	cf := addCommon(fs)
	timeout := fs.Duration("timeout", 30*time.Second, "таймаут регистрации")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := registration.NewClient()
	acct, err := ensureAccount(ctx, client, *cf.account, *cf.fresh, *cf.iface, *cf.debug)
	if err != nil {
		return err
	}

	if *cf.json {
		return printJSON(acct)
	}
	fmt.Printf("Аккаунт сохранён в %s\n", *cf.account)
	fmt.Printf("  IPv4: %s\n", acct.IPv4)
	fmt.Printf("  IPv6: %s\n", acct.IPv6)
	fmt.Printf("  Peer: %s\n", acct.PeerPublicKey)
	return nil
}

// --- scan ---

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	cf := addCommon(fs)
	pools := fs.String("pools", strings.Join(scanner.DefaultPools, ","), "диапазоны Cloudflare через запятую")
	ports := fs.String("ports", joinInts(scanner.DefaultPorts), "порты WARP через запятую")
	concurrency := fs.Int("concurrency", scanner.DefaultConcurrency, "сколько туннелей параллельно")
	maxCand := fs.Int("max", 512, "сколько кандидатов проверять за прогон (0 — все)")
	target := fs.Int("target", 3, "остановиться, когда найдено столько подходящих endpoint")
	handshake := fs.Duration("handshake", scanner.DefaultHandshakeTimeout, "таймаут хендшейка на кандидата")
	skipPing := fs.Bool("skip-ping", false, "не мерить задержку (только trace)")
	strictRegion := fs.Bool("strict-region", false, "отвергать endpoint'ы с регионом RU (по умолчанию учитываются)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := registration.NewClient()
	acct, err := ensureAccount(ctx, client, *cf.account, *cf.fresh, *cf.iface, *cf.debug)
	if err != nil {
		return err
	}

	prefixes, err := scanner.ParsePools(splitList(*pools))
	if err != nil {
		return err
	}
	portList, err := parsePorts(*ports)
	if err != nil {
		return err
	}
	cands := scanner.Expand(prefixes, portList)

	cfg := config.NewConfig()
	opts := scanner.Options{
		Concurrency:      *concurrency,
		MaxCandidates:    *maxCand,
		TargetGood:       *target,
		HandshakeTimeout: *handshake,
		SkipPing:         *skipPing,
		StrictRegion:     *strictRegion,
		OnProbe:          progressReporter(*cf.debug, len(cands)),
	}
	factory := newTunnelFactory(acct, cfg, *cf.iface)

	results, err := scanner.Scan(ctx, factory, cands, opts)
	if err != nil && len(results) == 0 {
		return err
	}
	ranked := scanner.Rank(results)

	if *cf.json {
		return printJSON(map[string]any{"candidates": len(cands), "results": results, "ranked": ranked})
	}

	fmt.Printf("Проверено кандидатов: %d\n", len(results))
	if len(results) > 0 {
		fmt.Printf("Итоги: %s\n", summarizeStatuses(results))
	}
	fmt.Printf("Подходящих endpoint: %d\n\n", len(ranked))
	if len(ranked) > 0 {
		fmt.Print(scanner.FormatTable(ranked))
		if n := countRU(ranked); n > 0 {
			fmt.Printf("\nПримечание: %d из %d endpoint'ов отдают внешним сервисам регион RU — WARP из России всегда так делает.\n", n, len(ranked))
		}
		return nil
	}
	if len(results) == 0 {
		fmt.Println("Ни один кандидат не прошёл даже хендшейк. Попробуйте другой --ports или проверьте, выключен ли посторонний VPN.")
		return nil
	}
	if allBadRegion(results) {
		fmt.Println("Рабочие endpoint'ы есть, но все они на выходе определяются как RU:")
		fmt.Println("Cloudflare назначает выходной IP по региону клиента, поэтому через WARP")
		fmt.Println("из России внешние сервисы всё равно видят российский регион (loc=RU).")
		fmt.Println("Это ограничение WARP, а не блокировка: сами endpoint и DNS работают.")
		return nil
	}
	fmt.Println("Подходящих endpoint не найдено. Попробуйте --max больше или другой --ports.")
	return nil
}

// countRU — сколько подходящих endpoint'ов определяются как регион RU.
func countRU(results []scanner.Result) int {
	n := 0
	for _, r := range results {
		if strings.EqualFold(strings.TrimSpace(r.Loc), "RU") {
			n++
		}
	}
	return n
}

// summarizeStatuses — раскладка результатов скана по статусам.
func summarizeStatuses(results []scanner.Result) string {
	counts := map[scanner.Status]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	type kv struct {
		s scanner.Status
		n int
	}
	xs := make([]kv, 0, len(counts))
	for s, n := range counts {
		xs = append(xs, kv{s, n})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].n > xs[j].n })
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%s — %d", x.s, x.n)
	}
	return strings.Join(parts, "; ")
}

// allBadRegion — есть endpoint'ы, дошедшие до trace, и все они отвергнуты только
// из-за региона RU. Тогда пустой вывод scan объясняется не блокировкой, а тем,
// что WARP из России всегда показывает RU.
func allBadRegion(results []scanner.Result) bool {
	reached := 0
	for _, r := range results {
		if r.Loc == "" {
			continue // кандидат не дошёл до trace — к региону отношения не имеет
		}
		reached++
		if r.Status != scanner.StatusBadRegion {
			return false
		}
	}
	return reached > 0
}

// --- dns-check ---

func runDNSCheck(args []string) error {
	fs := flag.NewFlagSet("dns-check", flag.ContinueOnError)
	cf := addCommon(fs)
	endpoint := fs.String("endpoint", "", "endpoint для проверки (ip:port); если пусто — сначала скан")
	dnsList := fs.String("dns", strings.Join(config.PresetDNS, ","), "DNS-серверы через запятую (IP или hostname)")
	timeout := fs.Duration("timeout", dnscheck.DefaultTimeout, "таймаут одной проверки")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := registration.NewClient()
	acct, err := ensureAccount(ctx, client, *cf.account, *cf.fresh, *cf.iface, *cf.debug)
	if err != nil {
		return err
	}

	cfg := config.NewConfig()
	tn, err := tunnel.New(tunnelOptions(acct, cfg, *cf.iface))
	if err != nil {
		return err
	}
	defer tn.Close()

	ep := strings.TrimSpace(*endpoint)
	if ep == "" {
		found, loc, err := findEndpoint(ctx, tn)
		if err != nil {
			return err
		}
		ep = found
		fmt.Printf("Выбран endpoint: %s (loc=%s)\n", ep, loc)
		if strings.EqualFold(loc, "RU") {
			fmt.Println("Примечание: выходной регион RU — так WARP видит Cloudflare из России.")
		}
	}
	if !tn.Handshake(ctx, ep, scanner.DefaultHandshakeTimeout) {
		return fmt.Errorf("не удалось установить хендшейк с %s", ep)
	}

	targets := buildDNSTargets(splitList(*dnsList))
	results := dnscheck.CheckAll(ctx, tn, targets, dnscheck.Options{Timeout: *timeout})

	if *cf.json {
		return printJSON(results)
	}
	fmt.Printf("Проверка DNS через туннель на %s:\n\n", ep)
	fmt.Print(dnscheck.FormatTable(results))
	return nil
}

// --- gen ---

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	cf := addCommon(fs)
	endpoint := fs.String("endpoint", "", "endpoint для конфига (ip:port), обязателен")
	dnsList := fs.String("dns", "1.1.1.1", "DNS-серверы через запятую")
	output := fs.String("output", "", "файл для .conf (по умолчанию stdout)")
	i1 := fs.String("i1", "", "I1-цепочка (по умолчанию генерируется QUIC-мимикрия)")
	randomJunk := fs.Bool("random-junk", false, "случайные Jc/Jmin/Jmax")
	skipVerify := fs.Bool("no-verify", false, "не проверять хендшейк перед генерацией")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*endpoint) == "" {
		return errors.New("укажите --endpoint (ip:port); подсказку даёт команда scan")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := registration.NewClient()
	acct, err := ensureAccount(ctx, client, *cf.account, *cf.fresh, *cf.iface, *cf.debug)
	if err != nil {
		return err
	}

	cfg := config.NewConfig()
	if *randomJunk {
		cfg.RandomizeJunk()
	}
	if strings.TrimSpace(*i1) != "" {
		cfg.SetI1(strings.TrimSpace(*i1))
	}
	if err := cfg.SetDNS(splitList(*dnsList)...); err != nil {
		return err
	}
	cfg.SetKeys(acct.PrivateKey)
	cfg.SetServerPublicKey(acct.PeerPublicKey)
	cfg.SetEndpoint(strings.TrimSpace(*endpoint))
	cfg.SetAddresses(acct.IPv4, acct.IPv6)

	if !*skipVerify {
		tn, err := tunnel.New(tunnelOptions(acct, cfg, *cf.iface))
		if err != nil {
			return err
		}
		ok := tn.Handshake(ctx, cfg.Endpoint, scanner.DefaultHandshakeTimeout)
		tn.Close()
		if !ok {
			return fmt.Errorf("endpoint %s не отвечает на хендшейк; выберите другой через scan или --no-verify", cfg.Endpoint)
		}
	}

	out, err := cfg.Generate()
	if err != nil {
		return fmt.Errorf("генерация конфига: %w", err)
	}

	if strings.TrimSpace(*output) == "" {
		_, err = os.Stdout.Write(out)
		return err
	}
	if err := os.WriteFile(*output, out, 0o600); err != nil {
		return err
	}
	fmt.Printf("Конфиг сохранён в %s\n", *output)
	return nil
}

// --- helpers ---

// ensureAccount загружает аккаунт, при необходимости регистрирует (напрямую или
// через туннель) и сохраняет.
func ensureAccount(ctx context.Context, client *http.Client, path string, fresh bool, iface string, debug bool) (registration.Account, error) {
	existing, err := registration.LoadOrRotate(path)
	if err != nil {
		return registration.Account{}, err
	}
	if !fresh && existing.Complete() {
		return existing, nil
	}

	if registration.APIReachable(ctx, client) {
		a, err := registration.Obtain(ctx, client, existing)
		if err != nil {
			return registration.Account{}, err
		}
		if err := registration.SaveAccount(path, a); err != nil {
			return registration.Account{}, err
		}
		return a, nil
	}

	if existing.Complete() {
		if debug {
			fmt.Fprintln(os.Stderr, "API Cloudflare недоступен напрямую — регистрируемся через туннель")
		}
		a, err := registerViaTunnel(ctx, existing, iface, debug)
		if err != nil {
			return registration.Account{}, err
		}
		if err := registration.SaveAccount(path, a); err != nil {
			return registration.Account{}, err
		}
		return a, nil
	}

	return registration.Account{}, errors.New(
		"API Cloudflare недоступен из этой сети; включите VPN/прокси либо укажите существующий --account для регистрации через туннель")
}

func newTunnelFactory(acct registration.Account, cfg config.Config, iface string) scanner.TunnelFactory {
	return func() (*tunnel.Tunnel, error) {
		return tunnel.New(tunnelOptions(acct, cfg, iface))
	}
}

func tunnelOptions(acct registration.Account, cfg config.Config, iface string) tunnel.Options {
	return tunnel.Options{
		PrivateKey:    acct.PrivateKey,
		PeerPublicKey: acct.PeerPublicKey,
		LocalV4:       acct.IPv4,
		LocalV6:       acct.IPv6,
		MTU:           cfg.MTU,
		Jc:            cfg.Jc,
		Jmin:          cfg.Jmin,
		Jmax:          cfg.Jmax,
		I1:            cfg.I1,
		Interface:     iface,
	}
}

const (
	// findEndpointMaxProbes — жёсткий предел перебора.
	findEndpointMaxProbes = 200
	// findEndpointGiveUp — после стольких проверок довольствуемся первым рабочим
	// endpoint, чтобы не искать не-RU регион бесконечно (из России его не бывает).
	findEndpointGiveUp = 40
)

// findEndpoint ищет рабочий endpoint для проверок (dns-check без --endpoint).
// Предпочитает endpoint с регионом не RU; если такого нет за отведённое число
// попыток — возвращает первый работающий (туннель проходит данные), чтобы
// проверка DNS всё равно состоялась.
func findEndpoint(ctx context.Context, tn *tunnel.Tunnel) (endpoint, loc string, err error) {
	cands := scanner.Expand(scanner.DefaultPrefixes(), scanner.DefaultPorts)
	firstWorking := ""
	firstLoc := ""
	probed := 0
	for _, c := range cands {
		if probed >= findEndpointMaxProbes || ctx.Err() != nil {
			break
		}
		if firstWorking != "" && probed >= findEndpointGiveUp {
			break
		}
		probed++
		ep := c.Endpoint()
		if !tn.Handshake(ctx, ep, scanner.DefaultHandshakeTimeout) {
			continue
		}
		tr, terr := tn.FetchTrace(ctx, scanner.DefaultTraceTimeout)
		if terr != nil {
			continue
		}
		if scanner.ClassifyRegion(tr.Loc, tr.Colo) == scanner.StatusOK {
			return ep, tr.Loc, nil
		}
		if firstWorking == "" {
			firstWorking, firstLoc = ep, tr.Loc
		}
	}
	if firstWorking != "" {
		return firstWorking, firstLoc, nil
	}
	return "", "", fmt.Errorf("не нашли рабочий endpoint за %d проверок; запустите scan и укажите --endpoint", probed)
}

func buildDNSTargets(names []string) []dnscheck.Target {
	var targets []dnscheck.Target
	for _, name := range names {
		ip, err := config.ResolveDNS(name)
		if err != nil {
			continue
		}
		targets = append(targets, dnscheck.Target{Name: name, IP: ip})
	}
	return targets
}

func progressReporter(debug bool, total int) func(done, total int) {
	if !debug {
		return nil
	}
	return func(done, t int) {
		fmt.Fprintf(os.Stderr, "\r  проверено %d/%d", done, t)
		if done >= t {
			fmt.Fprintln(os.Stderr)
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parsePorts(s string) ([]int, error) {
	var out []int
	for _, part := range splitList(s) {
		var p int
		if _, err := fmt.Sscanf(part, "%d", &p); err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("некорректный порт %q", part)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("пустой список портов")
	}
	return out, nil
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ",")
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printUsage — короткая справка.
func printUsage() {
	const usage = `warpgen — локальный генератор WARP-конфигов для Amnezia

Использование:
  warpgen [команда] [флаги]

Команды:
  register   Зарегистрировать WARP-аккаунт и сохранить в файл
  scan       Просканировать endpoint Cloudflare и показать подходящие
  dns-check  Проверить DNS через туннель на выбранном endpoint
  gen        Сгенерировать .conf для AmneziaWG
  chain      Запустить warp-plus (WARP → Psiphon) поверх внешнего AWG-туннеля
  version    Версия утилиты
  help       Показать эту справку

Общие флаги:
  -account   Файл аккаунта WARP (по умолчанию warpgen-account.json)
  -fresh     Зарегистрировать новый аккаунт даже при наличии файла
  -interface Привязать UDP-сокет к интерфейсу (linux, нужен root)
  -json      Вывод в JSON
  -debug     Подробный вывод

Ограничения:
  - Если на устройстве активен VPN, скан пойдёт через него.
  - Не полагаемся на ICMP — задержку измеряем внутри туннеля.
  - WARP-safe инварианты: S1..S4 = 0, H1..H4 = 1..4, MTU = 1280.
`
	fmt.Print(usage)
}
