package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// Options — параметры сборки рабочей конфигурации (уровень CLI).
type Options struct {
	// AccountFile — файл аккаунта warpgen (ключи туннеля и identity warp-plus).
	AccountFile string
	// WarpPlus — путь к бинарнику warp-plus ("" — искать в PATH и рядом с warpgen).
	WarpPlus string
	// CacheDir — каталог identity warp-plus и кэша Psiphon.
	CacheDir string
	// Countries — страны Psiphon в порядке приоритета (в WarpOnly не нужны).
	Countries []string
	// WarpOnly — режим без Psiphon: наружу отдаётся локальный SOCKS5 поверх
	// внешнего WARP-туннеля с обфускацией AWG.
	WarpOnly bool
	// Bind — адрес SOCKS5: в обычном режиме его слушает warp-plus, в WarpOnly —
	// локальный сервер поверх туннеля.
	Bind string
	// Listen — адрес UDP-форвардера (loopback, порт 0 — свободный).
	Listen string
	// InnerEndpoint — целевой адрес внутреннего WARP (для форвардера).
	InnerEndpoint string
	// Endpoints — сколько кандидатов внешних endpoint'ов использовать.
	Endpoints int
	// MaxCandidates — сколько кандидатов проверять за скан.
	MaxCandidates int
	// StrictRegion — жёсткий режим скана: отвергать endpoint'ы с регионом RU.
	StrictRegion bool
	// EndpointCache — файл кэша выбранных endpoint'ов ("" — без кэша).
	EndpointCache string
	// SpeedURL / SpeedBytes / SpeedTimeout — параметры замера скорости при
	// первичной настройке кэша.
	SpeedURL     string
	SpeedBytes   int64
	SpeedTimeout time.Duration

	// TestURL / ReadyTimeout / Attempts / Strict — параметры warp-plus (см. chain).
	TestURL      string
	ReadyTimeout time.Duration
	Attempts     int
	Strict       bool

	// TUN — режим системного туннеля (--tun): весь трафик машины идёт по цепочке
	// TUN → мост → SOCKS5 → WARP → Cloudflare.
	TUN bool
	// TUNName / TUNMTU — имя и MTU TUN-интерфейса.
	TUNName string
	TUNMTU  int
	// TUNDevice — готовое TUN-устройство. На Android fd даёт VpnService;
	// на Linux пусто — устройство создаётся само.
	TUNDevice vpn.Device
	// TUNCheckPrivileges — проверка прав для TUN. На Android VpnService уже
	// выдал права, поэтому проверка подменяется на пустую.
	TUNCheckPrivileges func() error
	// TUNProtect — защита исходящих сокетов от собственного VPN (Android:
	// VpnService.protect). На Linux не нужна.
	TUNProtect func(fd int) error
	// DNS — резолверы-кандидаты для DNS через цепочку (ip[:port]).
	DNS []string
	// UDP — политика обработки UDP (поддерживается drop).
	UDP string
	// DryRun — напечатать системные команды и ничего не менять в системе.
	DryRun bool

	// Logf — приёмник служебных сообщений (только диагностика).
	Logf func(format string, args ...any)
	// Clock — часы (nil — системные).
	Clock Clock
	// Тайминги/лимиты пайплайна (0 — значения по умолчанию).
	MonitorInterval time.Duration
	MonitorFailures int
	TraceFailures   int
	RUFailures      int
	RetryPause      time.Duration
	SessionTTL      time.Duration
	MaxSessions     int
}

// NewConfig собирает рабочую конфигурацию пайплайна на реальных компонентах:
// userspace-туннель, скан endpoint'ов, цепочку warp-plus и проверку выхода.
//
// Сетевых запросов не делает: локальные проверки (аккаунт, identity, бинарник)
// выполняет Preflight уже внутри Run.
func NewConfig(opts Options) (Config, error) {
	if len(opts.Countries) == 0 && !opts.WarpOnly {
		return Config{}, errors.New("не задан список стран (--countries)")
	}
	// Каталог кэша нормализуем сразу: от него зависят и preflight, и цепочка,
	// а "" превратился бы в относительный primary/ рядом с текущим каталогом.
	opts.CacheDir = firstNonEmpty(opts.CacheDir, chain.DefaultCacheDir())
	// WARP-safe параметры обфускации и MTU — те же, что у команды gen.
	warpCfg := config.NewConfig()

	newTunnel := func() (*tunnel.Tunnel, error) {
		acct, err := registration.LoadAccount(opts.AccountFile)
		if err != nil {
			return nil, fmt.Errorf("аккаунт %s: %w", opts.AccountFile, err)
		}
		return tunnel.New(tunnel.Options{
			PrivateKey:    acct.PrivateKey,
			PeerPublicKey: acct.PeerPublicKey,
			LocalV4:       acct.IPv4,
			LocalV6:       acct.IPv6,
			MTU:           warpCfg.MTU,
			Jc:            warpCfg.Jc,
			Jmin:          warpCfg.Jmin,
			Jmax:          warpCfg.Jmax,
			I1:            warpCfg.I1,
		})
	}

	logf := opts.Logf
	var onProbe func(done, total int)
	if logf != nil {
		onProbe = func(done, total int) { logf("\r  проверено %d/%d", done, total) }
	}

	cfg := Config{
		Direct:           opts.WarpOnly,
		Bind:             opts.Bind,
		InnerEndpoint:    firstNonEmpty(opts.InnerEndpoint, DefaultInnerEndpoint),
		Listen:           firstNonEmpty(opts.Listen, DefaultListen),
		Countries:        append([]string(nil), opts.Countries...),
		Endpoints:        opts.Endpoints,
		Tunnel:           func() (Tunnel, error) { return newTunnel() },
		Locator:          scannerLocator{factory: newTunnel, maxCandidates: opts.MaxCandidates, strictRegion: opts.StrictRegion, onProbe: onProbe},
		Chain:            chainRunner{opts: chainOptions(opts)},
		Prober:           geoProber{services: chain.DefaultGeoServices},
		Clock:            opts.Clock,
		ProfilePath:      opts.EndpointCache,
		SpeedURL:         opts.SpeedURL,
		SpeedBytes:       opts.SpeedBytes,
		SpeedTimeout:     opts.SpeedTimeout,
		Logf:             opts.Logf,
		Preflight:        preflightFn(opts),
		MTU:              warpCfg.MTU,
		MonitorInterval:  opts.MonitorInterval,
		MonitorFailures:  opts.MonitorFailures,
		TraceFailures:    opts.TraceFailures,
		RUFailures:       opts.RUFailures,
		RetryPause:       opts.RetryPause,
		SessionTTL:       opts.SessionTTL,
		MaxSessions:      opts.MaxSessions,
		HandshakeTimeout: scanner.DefaultHandshakeTimeout,
		TraceTimeout:     scanner.DefaultTraceTimeout,
	}

	if opts.TUN {
		// Системный туннель собирается сразу: проверки (права, адрес, политика
		// UDP, список резолверов) должны провалиться до сетевых действий.
		sys, err := vpn.New(vpn.Options{
			TUNName:         opts.TUNName,
			MTU:             opts.TUNMTU,
			DNS:             opts.DNS,
			UDP:             vpn.UDPPolicy(opts.UDP),
			DryRun:          opts.DryRun,
			Device:          opts.TUNDevice,
			CheckPrivileges: opts.TUNCheckPrivileges,
			Protect:         opts.TUNProtect,
			Logf:            opts.Logf,
		})
		if err != nil {
			return Config{}, err
		}
		cfg.TUN = func() (TUNSystem, error) { return sys, nil }
	}
	return cfg, nil
}

// chainOptions переносит параметры цепочки (endpoint подставляет ChainRunner).
func chainOptions(opts Options) chain.Options {
	return chain.Options{
		WarpPlus:     opts.WarpPlus,
		Countries:    append([]string(nil), opts.Countries...),
		Bind:         opts.Bind,
		CacheDir:     opts.CacheDir,
		TestURL:      opts.TestURL,
		ReadyTimeout: opts.ReadyTimeout,
		Attempts:     opts.Attempts,
		Strict:       opts.Strict,
		AccountFile:  opts.AccountFile,
	}
}

// preflightFn выбирает локальные проверки под режим. В WarpOnly warp-plus не
// участвует, поэтому его identity и бинарник не нужны — только аккаунт.
func preflightFn(opts Options) func(context.Context) error {
	if opts.WarpOnly {
		return func(context.Context) error { return checkAccount(opts.AccountFile) }
	}
	return func(context.Context) error { return preflight(opts.AccountFile, opts.CacheDir, opts.WarpPlus) }
}

// preflight — только локальные проверки: аккаунт warpgen, identity для warp-plus,
// бинарник warp-plus. В сеть не ходит.
func preflight(accountFile, cacheDir, warpPlus string) error {
	if err := checkAccount(accountFile); err != nil {
		return err
	}
	if err := chain.EnsureIdentity(cacheDir, accountFile); err != nil {
		return err
	}
	if _, err := chain.ResolveWarpPlus(warpPlus); err != nil {
		return err
	}
	return nil
}

// checkAccount — аккаунт warpgen есть и читается.
func checkAccount(accountFile string) error {
	if _, err := registration.LoadAccount(accountFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("нет аккаунта warpgen (%s): сгенерируйте командой `warpgen register` или укажите --account", accountFile)
		}
		return err
	}
	return nil
}

// scannerLocator ищет endpoint'ы тем же сканом, что и команда scan: предфильтр
// хендшейком, trace через туннель, ранжирование по задержке. Российские colo
// DME/LED/KJA отсекаются внутри скана.
type scannerLocator struct {
	factory       scanner.TunnelFactory
	maxCandidates int
	strictRegion  bool
	onProbe       func(done, total int)
}

func (l scannerLocator) Locate(ctx context.Context, limit int) ([]Endpoint, error) {
	if limit <= 0 {
		limit = DefaultEndpoints
	}
	if l.factory == nil {
		return nil, errors.New("не задан фабрикатор туннеля для скана")
	}
	cands := scanner.Expand(scanner.DefaultPrefixes(), scanner.DefaultPorts)
	results, err := scanner.Scan(ctx, l.factory, cands, scanner.Options{
		MaxCandidates: l.maxCandidates,
		TargetGood:    limit,
		StrictRegion:  l.strictRegion,
		OnProbe:       l.onProbe,
	})
	if err != nil && len(results) == 0 {
		return nil, err
	}

	ranked := scanner.Rank(results)
	out := make([]Endpoint, 0, limit)
	for _, r := range ranked {
		out = append(out, Endpoint{Addr: r.Endpoint(), Loc: r.Loc, Colo: r.Colo, RTT: r.RTT})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// chainRunner запускает цепочку поверх локального форвардера (внешний туннель уже
// поднят в процессе, поэтому проверка системного маршрута не нужна).
type chainRunner struct {
	opts chain.Options
}

func (c chainRunner) Run(ctx context.Context, req ChainRequest) (*ChainSession, error) {
	opts := c.opts
	opts.Endpoint = req.Forwarder
	opts.Countries = req.Countries
	if req.Bind != "" {
		opts.Bind = req.Bind
	}

	sess, err := chain.RunThrough(ctx, opts)
	if err != nil {
		return nil, chainReason(err)
	}
	return &ChainSession{
		SOCKS:   sess.Bind,
		Country: sess.Country,
		IP:      sess.IP,
		Org:     sess.Org,
		Elapsed: sess.Elapsed,
		Close:   sess.Close,
	}, nil
}

// chainReason сжимает таблицу попыток chain в одну строку причины.
func chainReason(err error) error {
	var fail *chain.Failure
	if errors.As(err, &fail) && len(fail.Attempts) > 0 {
		reasons := make([]string, 0, len(fail.Attempts))
		for _, a := range fail.Attempts {
			reasons = append(reasons, fmt.Sprintf("%s: %s", a.Country, a.Reason))
		}
		return errors.New(strings.Join(reasons, "; "))
	}
	return err
}

// livenessTimeout — потолок на проверку связи через SOCKS5.
const livenessTimeout = 15 * time.Second

// geoProber проверяет выход через SOCKS5 теми же геосервисами и критерием, что и
// готовность цепочки.
//
// Проверка разделена на две части: «ходит ли трафик» и «какая страна на выходе».
// Геосервисы могут молчать сами по себе (429, челлендж, блокировка на общем
// выходном IP WARP) — это не сломанная цепочка, и на таком молчании менять звено
// нельзя. Поэтому пустой ответ геосервисов подтверждаем отдельным запросом
// (liveness) и отдаём Services=0: мониторинг увидит «страну не знаем» и не будет
// переключаться.
type geoProber struct {
	services []chain.GeoService
}

func (p geoProber) Probe(ctx context.Context, socks string) (ProbeResult, error) {
	client, err := chain.NewGeoClient(socks)
	if err != nil {
		return ProbeResult{}, err
	}
	services := p.services
	if len(services) == 0 {
		services = chain.DefaultGeoServices
	}

	// strict=false: для мониторинга важно только «не RU»; совпадение со страной
	// проверено на этапе готовности.
	results, err := chain.Evaluate(ctx, client, services, "", false)
	res := ProbeResult{Services: len(results)}
	for i, r := range results {
		if r.Country == "RU" {
			res.RU = true
		}
		if i == 0 {
			res.Country, res.IP, res.Org = r.Country, r.IP, r.Org
		}
	}

	if len(results) == 0 {
		// Геосервисы молчат. Если трафик через SOCKS5 всё же ходит — это слепая
		// проверка, а не сбой: возвращаем пустой результат без ошибки.
		if lerr := probeLiveness(ctx, client); lerr != nil {
			return ProbeResult{}, fmt.Errorf("геосервисы не ответили (%v) и связи через SOCKS5 нет: %w", err, lerr)
		}
		return ProbeResult{}, nil
	}
	if err != nil && !res.RU {
		return res, err
	}
	return res, nil
}

// probeLiveness проверяет, что через SOCKS5 вообще проходит HTTPS-запрос
// (cdn-cgi/trace Cloudflare — тот же узел, что у внешнего туннеля).
func probeLiveness(ctx context.Context, client *http.Client) error {
	reqCtx, cancel := context.WithTimeout(ctx, livenessTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, tunnel.TraceURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("trace через SOCKS5: статус %d", resp.StatusCode)
	}
	return nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
