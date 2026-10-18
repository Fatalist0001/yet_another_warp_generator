// Package chain — цепочка «внешний AWG-туннель → warp-plus (WARP) → Psiphon».
//
// При уже поднятом внешнем туннеле (например, конфиг warpgen в AmneziaVPN) пакет
// запускает внешний бинарник warp-plus в режиме --cfon, дожидается, пока через
// него заработает SOCKS5 с выходом в нужной стране, и возвращает живую сессию.
// Код warp-plus и Psiphon в проект не вшивается — только вызов бинарника.
package chain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

// DefaultCountries — страны по умолчанию, в порядке приоритета.
//
// FI намеренно не входит: на замерах Psiphon-этап там стабильно не поднимается
// («tunnel establishment timeout»), страну можно запросить вручную через --countries.
var DefaultCountries = []string{"NL", "SE", "DE", "US", "PL"}

// Значения по умолчанию для команды chain.
const (
	DefaultEndpoint     = "162.159.192.1:2408"
	DefaultBind         = "127.0.0.1:8086"
	DefaultTestURL      = "http://example.com/"
	DefaultReadyTimeout = 100 * time.Second
	DefaultAttempts     = 2

	// legacyCacheDir — прежний каталог по умолчанию: относительная папка рядом
	// с текущим каталогом. Оставлена как запасной вариант, если домашний каталог
	// недоступен.
	legacyCacheDir = "warpgen-chain"
)

// DefaultCacheDir — каталог identity warp-plus и кэша Psiphon по умолчанию:
// ~/.warpgen/chain.
//
// Раньше это была относительная папка warpgen-chain рядом с текущим каталогом:
// запуск `sudo ./warpgen run` из корня репозитория оставлял там root-овый каталог,
// который потом мешал gofmt и обходу репозитория. Абсолютный путь в домашнем
// каталоге этой проблемы не создаёт (у root он свой — /root/.warpgen).
func DefaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return legacyCacheDir
	}
	return filepath.Join(home, ".warpgen", "chain")
}

// pollInterval — пауза между опросами готовности внутри одной попытки.
const pollInterval = 1500 * time.Millisecond

// portReleaseTimeout — сколько ждём освобождения порта SOCKS5 между попытками.
const portReleaseTimeout = 5 * time.Second

// Options — параметры запуска цепочки.
type Options struct {
	// WarpPlus — путь к бинарнику warp-plus ("" — искать в PATH и рядом с warpgen).
	WarpPlus string
	// Countries — страны в порядке приоритета (ISO-3166 alpha-2).
	Countries []string
	// Endpoint — WARP-endpoint для внутреннего туннеля.
	Endpoint string
	// Bind — адрес SOCKS5-прокси, который поднимает warp-plus.
	Bind string
	// CacheDir — каталог identity warp-plus и кэша Psiphon.
	CacheDir string
	// TestURL — URL проверки связи внутри warp-plus (нужен HEAD=200).
	TestURL string
	// ReadyTimeout — сколько ждать готовности одной попытки.
	ReadyTimeout time.Duration
	// Attempts — число попыток на страну.
	Attempts int
	// Strict — требовать совпадения страны с запрошенной.
	Strict bool
	// AccountFile — файл аккаунта warpgen (источник identity для warp-plus).
	AccountFile string
}

func (o Options) withDefaults() Options {
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	if o.Bind == "" {
		o.Bind = DefaultBind
	}
	if o.CacheDir == "" {
		o.CacheDir = DefaultCacheDir()
	}
	if o.TestURL == "" {
		o.TestURL = DefaultTestURL
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = DefaultReadyTimeout
	}
	if o.Attempts <= 0 {
		o.Attempts = DefaultAttempts
	}
	if len(o.Countries) == 0 {
		o.Countries = append([]string(nil), DefaultCountries...)
	}
	if o.AccountFile == "" {
		o.AccountFile = registration.DefaultAccountFile
	}
	return o
}

// Attempt — одна неудачная попытка страны.
type Attempt struct {
	Country string
	Number  int
	Reason  string
}

// Failure — ни одна из стран не прошла; несёт таблицу попыток для отчёта.
type Failure struct {
	Attempts []Attempt
}

func (f *Failure) Error() string {
	return fmt.Sprintf("ни одна из стран не прошла (%d попыток)", len(f.Attempts))
}

// Session — успешная сессия: живой warp-plus с работающим SOCKS5.
type Session struct {
	// Bind — адрес SOCKS5-прокси.
	Bind string
	// Country — запрошенная (и подтверждённая) страна.
	Country string
	// IP — внешний IPv4, который увидели геосервисы.
	IP string
	// Org — организация/ASN выхода.
	Org string
	// Elapsed — сколько заняла подготовка от запуска процесса.
	Elapsed time.Duration
	// Queries — ответы геосервисов (может расходиться по стране).
	Queries []GeoResult
	// Attempts — неудачные попытки, предшествовавшие успеху.
	Attempts []Attempt

	proc *child
}

// Close корректно завершает дочерний процесс (всю группу процессов).
func (s *Session) Close() error {
	if s == nil || s.proc == nil {
		return nil
	}
	return s.proc.Stop()
}

// attemptFailure — причина сбоя одной попытки с человекочитаемой меткой.
type attemptFailure struct {
	Reason string
	Err    error
}

// tryFunc запускает одну попытку страны (вынесено для тестируемости).
type tryFunc func(ctx context.Context, country string) (*Session, *attemptFailure)

// Run выполняет всю цепочку. На успехе возвращает живую сессию (её нужно закрыть);
// при сбое всех стран ошибка имеет тип *Failure с таблицей попыток.
//
// Требует, чтобы внешний туннель уже был поднят по системному маршруту
// (AmneziaVPN и т.п.): это проверяется запросом trace.
func Run(ctx context.Context, opts Options) (*Session, error) {
	opts = opts.withDefaults()
	if err := checkCountries(opts); err != nil {
		return nil, err
	}
	if _, err := checkOuterTunnel(ctx, defaultHTTPClient(), tunnel.TraceURL); err != nil {
		return nil, err
	}
	return runThrough(ctx, opts)
}

// RunThrough — то же, что Run, но без проверки внешнего туннеля по системному
// маршруту. Нужна, когда внешний слой поднимает сама вызывающая сторона и трафик
// уходит не по системному маршруту: например, userspace-туннель `warpgen run`,
// который отдаёт warp-plus локальный UDP-форвардер (opts.Endpoint).
func RunThrough(ctx context.Context, opts Options) (*Session, error) {
	opts = opts.withDefaults()
	if err := checkCountries(opts); err != nil {
		return nil, err
	}
	return runThrough(ctx, opts)
}

func checkCountries(opts Options) error {
	if len(opts.Countries) == 0 {
		return errors.New("пустой список стран (--countries)")
	}
	return nil
}

// runThrough готовит identity warp-plus, находит бинарник и перебирает страны.
func runThrough(ctx context.Context, opts Options) (*Session, error) {
	if err := EnsureIdentity(opts.CacheDir, opts.AccountFile); err != nil {
		return nil, err
	}
	bin, err := ResolveWarpPlus(opts.WarpPlus)
	if err != nil {
		return nil, err
	}
	try := func(ctx context.Context, cc string) (*Session, *attemptFailure) {
		return tryCountry(ctx, opts, bin, cc)
	}
	return runCountries(ctx, opts, try)
}

// runCountries перебирает страны в порядке приоритета, делая до Attempts попыток
// на каждую, и останавливается на первой успешной.
func runCountries(ctx context.Context, opts Options, try tryFunc) (*Session, error) {
	var attempts []Attempt
	for _, cc := range opts.Countries {
		for n := 1; n <= opts.Attempts; n++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			sess, fail := try(ctx, cc)
			if fail == nil {
				sess.Attempts = attempts
				return sess, nil
			}
			attempts = append(attempts, Attempt{Country: cc, Number: n, Reason: fail.Reason})
		}
	}
	return nil, &Failure{Attempts: attempts}
}

// tryCountry запускает warp-plus на одну страну и ждёт готовности прокси.
func tryCountry(ctx context.Context, opts Options, bin, country string) (*Session, *attemptFailure) {
	start := time.Now()
	logPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("warpgen-chain-%s-%d.log", country, time.Now().UnixNano()))

	// Флаги сверены с отчётом о стенде: -4 (IPv4), обходной --test-url (HEAD=200),
	// вывод в лог-файл — чтобы на экран не попали служебные строки.
	args := []string{
		"-4",
		"-e", opts.Endpoint,
		"-b", opts.Bind,
		"--cfon",
		"--country", country,
		"--test-url", opts.TestURL,
		"--cache-dir", opts.CacheDir,
		"-v",
	}
	c, err := startChild(bin, args, logPath)
	if err != nil {
		return nil, &attemptFailure{Reason: "не удалось запустить warp-plus", Err: err}
	}

	client, err := NewGeoClient(opts.Bind)
	if err != nil {
		_ = c.Stop()
		return nil, &attemptFailure{Reason: "SOCKS5-клиент", Err: err}
	}

	results, err := waitReady(ctx, c, client, country, opts, start)
	if err != nil {
		reason := readinessReason(err, c.log())
		_ = c.Stop()
		waitPortFree(opts.Bind, portReleaseTimeout)
		return nil, &attemptFailure{Reason: reason, Err: err}
	}

	primary := pickPrimary(results, country)
	return &Session{
		Bind:    opts.Bind,
		Country: country,
		IP:      primary.IP,
		Org:     primary.Org,
		Elapsed: time.Since(start),
		Queries: results,
		proc:    c,
	}, nil
}

// waitReady опрашивает геосервисы через SOCKS5 до готовности или таймаута.
func waitReady(ctx context.Context, c *child, client *http.Client, country string, opts Options, start time.Time) ([]GeoResult, error) {
	deadline := start.Add(opts.ReadyTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.Alive() {
			return nil, errors.New("warp-plus завершился до готовности")
		}
		if results, err := Evaluate(ctx, client, DefaultGeoServices, country, opts.Strict); err == nil {
			return results, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("готовность не достигнута за %s", opts.ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// readinessReason переводит ошибку готовности в метку для таблицы попыток.
func readinessReason(err error, logContent string) string {
	switch {
	case errors.Is(err, errExitRU):
		return "не та страна (выход RU)"
	case errors.Is(err, errWrongCountry):
		return "не та страна"
	}
	return classifyLog(logContent)
}

// classifyLog по логу warp-plus определяет звено, на котором сорвалась попытка.
// Сам лог на экран не выводится — в нём могут быть служебные строки.
func classifyLog(content string) string {
	switch {
	case content == "":
		return "нет данных (лог пуст)"
	case strings.Contains(content, "tunnel establishment timeout"):
		return "Psiphon не построил туннель"
	case strings.Contains(content, "connection test failed"):
		return "гейт warp-plus (--test-url)"
	case strings.Contains(content, "waiting on handshake"):
		return "нет хендшейка WARP"
	default:
		return "готовность не достигнута"
	}
}

// pickPrimary выбирает основной ответ геосервиса — тот, что совпал со страной.
func pickPrimary(results []GeoResult, country string) GeoResult {
	for _, r := range results {
		if r.Country == strings.ToUpper(country) {
			return r
		}
	}
	if len(results) > 0 {
		return results[0]
	}
	return GeoResult{}
}

// waitPortFree ждёт, пока порт SOCKS5 освободится после остановки warp-plus.
func waitPortFree(bind string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", bind)
		if err == nil {
			_ = ln.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ResolveWarpPlus ищет бинарник warp-plus: явный путь → PATH → каталог рядом с warpgen.
// Экспортирована для локального preflight'а (пакет core проверяет бинарник до старта).
func ResolveWarpPlus(explicit string) (string, error) {
	if explicit != "" {
		if err := checkExecutable(explicit); err != nil {
			return "", fmt.Errorf("warp-plus %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if p, err := exec.LookPath("warp-plus"); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "warp-plus")
		if err := checkExecutable(cand); err == nil {
			return cand, nil
		}
	}
	return "", errors.New("не нашли бинарник warp-plus: положите его в PATH или рядом с warpgen либо укажите --warp-plus")
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("это каталог, а не бинарник")
	}
	return nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: outerTraceTimeout}
}

// FormatFailures — таблица попыток с причинами сбоя.
func FormatFailures(attempts []Attempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-8s %s\n", "СТРАНА", "ПОПЫТКА", "ПРИЧИНА")
	for _, a := range attempts {
		fmt.Fprintf(&b, "%-8s %-8d %s\n", a.Country, a.Number, a.Reason)
	}
	return b.String()
}
