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
	"runtime"
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

// SupportedCountries — коды, которые принимает `--country` у warp-plus v1.2.6.
//
// Флаг объявлен в его CLI как enum: неизвестный код (например, RU) роняет процесс
// ещё до сети, и без этой проверки падение выглядело бы как «готовность не
// достигнута». Список сверен с psiphon.Countries в исходниках v1.2.6.
var SupportedCountries = []string{
	"AT", "AU", "BE", "BG", "CA", "CH", "CZ", "DE", "DK", "EE",
	"ES", "FI", "FR", "GB", "HR", "HU", "IE", "IN", "IT", "JP",
	"LV", "NL", "NO", "PL", "PT", "RO", "RS", "SE", "SG", "SK", "US",
}

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
	// Logf — приёмник служебных сообщений (путь к логу warp-plus и причина сбоя
	// попытки). nil — молча; секретов в сообщениях нет.
	Logf func(format string, args ...any)
}

// logf пишет служебное сообщение, если приёмник задан.
func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
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
	// Established — Psiphon поднял туннель, но выход оказался другой страной
	// (не-RU): повод добрать список с мягким критерием.
	Established bool
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
	// Country — страна, которую фактически видит выход. В мягком режиме (страны
	// кончились, взяли любой выход вне РФ) может отличаться от Requested.
	Country string
	// Requested — страна, которую просили в начале попытки.
	Requested string
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
	// Established — Psiphon поднял туннель, но выход оказался другой (не-RU)
	// страной. По этой метке решается, стоит ли добирать страны с мягким
	// критерием (любой выход вне РФ).
	Established bool
	// Country — страну, которую реально отдал выход (если её удалось узнать).
	Country string
}

// tryFunc запускает одну попытку страны (вынесено для тестируемости).
type tryFunc func(ctx context.Context, opts Options, country string) (*Session, *attemptFailure)

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
	return ValidateCountries(opts.Countries)
}

// ValidateCountries проверяет список стран Psiphon: пустой список и коды, которых
// нет в warp-plus v1.2.6, отсеиваются до сети (его `--country` — enum, и
// неизвестный код роняет процесс без внятного сообщения).
// Экспортирована для ранней проверки в CLI: в `run` она идёт до скана endpoint'ов.
func ValidateCountries(codes []string) error {
	if len(codes) == 0 {
		return errors.New("пустой список стран (--countries)")
	}
	var unknown []string
	for _, cc := range codes {
		code := strings.ToUpper(strings.TrimSpace(cc))
		if !supportedCountry(code) {
			unknown = append(unknown, code)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("warp-plus v1.2.6 не знает стран %s; допустимые: %s",
			strings.Join(unknown, ", "), strings.Join(SupportedCountries, ", "))
	}
	return nil
}

func supportedCountry(code string) bool {
	for _, cc := range SupportedCountries {
		if cc == code {
			return true
		}
	}
	return false
}

// runThrough готовит identity warp-plus, находит бинарник и перебирает страны.
//
// После первой же неудачной попытки кэш Psiphon сбрасывается (identity остаётся):
// убитый по таймауту warp-plus оставляет недописанное состояние, из-за которого
// следующая попытка валится тем же местом, хотя сеть уже в порядке.
func runThrough(ctx context.Context, opts Options) (*Session, error) {
	if err := EnsureIdentity(opts.CacheDir, opts.AccountFile); err != nil {
		return nil, err
	}
	bin, err := ResolveWarpPlus(opts.WarpPlus)
	if err != nil {
		return nil, err
	}
	// Остаточный warp-plus от прошлого запуска (warpgen убили жёстко, остановка не
	// отработала) держит порт SOCKS5 и файлы кэша: без зачистки новая попытка
	// падает с «порт занят», а сброс кэша — с «не удалось сбросить кэш Psiphon».
	if killed := KillStaleWarpPlus(bin); len(killed) > 0 {
		opts.logf("остаточный warp-plus от прошлого запуска: завершён %s", strings.Join(killed, ", "))
		waitPortFree(opts.Bind, portReleaseTimeout)
	}

	attempted := false
	try := func(ctx context.Context, opts Options, cc string) (*Session, *attemptFailure) {
		if attempted {
			if err := resetPsiphonState(opts.CacheDir); err != nil {
				return nil, &attemptFailure{Reason: "не удалось сбросить кэш Psiphon", Err: err}
			}
		}
		attempted = true
		return tryCountry(ctx, opts, bin, cc)
	}

	return runWithFallback(ctx, opts, try)
}

// runWithFallback перебирает страны штатно, а если Psiphon поднимался, но ни одна
// запрошенная страна не совпала с выходом, повторяет список с мягким критерием:
// годится любой выход вне РФ. Запрошенные страны остаются приоритетом — меняется
// только критерий приёмки, и только когда без этого не осталось бы ничего.
func runWithFallback(ctx context.Context, opts Options, try tryFunc) (*Session, error) {
	sess, err := runCountries(ctx, opts, try)
	if err == nil {
		return sess, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	var fail *Failure
	if !opts.Strict || !errors.As(err, &fail) || !needsSoftPass(fail.Attempts) {
		return nil, err
	}
	soft := opts
	soft.Strict = false
	return runCountriesFrom(ctx, soft, try, fail.Attempts)
}

// needsSoftPass — хотя бы одна попытка дошла до готового туннеля Psiphon, но
// выход оказался другой страной. Только в этом случае имеет смысл мягкий добор:
// если Psiphon вовсе не поднимался или выход RU, второй проход ничего не изменит.
func needsSoftPass(attempts []Attempt) bool {
	for _, a := range attempts {
		if a.Established {
			return true
		}
	}
	return false
}

// resetPsiphonState удаляет накопленное состояние Psiphon в каталоге кэша,
// сохраняя identity warp-plus (подкаталог primary). warp-plus держит identity и
// кэш Psiphon в одном каталоге, поэтому убираем всё, кроме identity.
//
// Повторы нужны под Windows: занятый живым процессом файл (остаточный warp-plus,
// антивирусный сканер, индексатор) удалить не даёт, а состояние освобождается
// само через доли секунды. Настоящая блокировка живым warp-plus снимается
// зачисткой KillStaleWarpPlus до первой попытки.
func resetPsiphonState(cacheDir string) error {
	var firstErr error
	for attempt := 0; attempt < resetAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(resetPause)
		}
		entries, err := os.ReadDir(cacheDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		firstErr = nil
		for _, entry := range entries {
			if entry.IsDir() && strings.EqualFold(entry.Name(), identityDirName) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(cacheDir, entry.Name())); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if firstErr == nil {
			return nil
		}
	}
	return fmt.Errorf("файл кэша Psiphon занят другим процессом: %w", firstErr)
}

const (
	// resetAttempts / resetPause — повторы сброса кэша Psiphon (см. resetPsiphonState).
	resetAttempts = 4
	resetPause    = 300 * time.Millisecond
)

// runCountries перебирает страны в порядке приоритета, делая до Attempts попыток
// на каждую, и останавливается на первой успешной.
func runCountries(ctx context.Context, opts Options, try tryFunc) (*Session, error) {
	return runCountriesFrom(ctx, opts, try, nil)
}

// runCountriesFrom — то же, но продолжает отчёт попыток с переданного списка
// (используется второй проход с мягким критерием).
func runCountriesFrom(ctx context.Context, opts Options, try tryFunc, prior []Attempt) (*Session, error) {
	attempts := append([]Attempt(nil), prior...)
	for _, cc := range opts.Countries {
		for n := 1; n <= opts.Attempts; n++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			sess, fail := try(ctx, opts, cc)
			if fail == nil {
				sess.Attempts = attempts
				return sess, nil
			}
			attempts = append(attempts, Attempt{
				Country:     cc,
				Number:      n,
				Reason:      fail.Reason,
				Established: fail.Established,
			})
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
		logContent := c.log()
		fail := &attemptFailure{Reason: describeFailure(err, logContent), Err: err}
		// Туннель Psiphon поднялся, но выход не подошёл — это не поломка звена,
		// а повод попробовать другую страну и (потом) мягкий критерий.
		var we *wrongExit
		if errors.As(err, &we) {
			fail.Country = we.got
			// Мягкий добор имеет смысл только когда выход живой, но не та страна:
			// при выходе RU он и с мягким критерием не пройдёт.
			fail.Established = errors.Is(err, errWrongCountry)
		}
		opts.logf("warp-plus (%s): %s; лог: %s", country, fail.Reason, logPath)
		_ = c.Stop()
		waitPortFree(opts.Bind, portReleaseTimeout)
		return nil, fail
	}

	primary := pickPrimary(results, country)
	observed := primary.Country
	if observed == "" {
		observed = country
	}
	return &Session{
		Bind:      opts.Bind,
		Country:   observed,
		Requested: country,
		IP:        primary.IP,
		Org:       primary.Org,
		Elapsed:   time.Since(start),
		Queries:   results,
		proc:      c,
	}, nil
}

// errProcessGone — warp-plus завершился, не дав рабочего SOCKS5.
var errProcessGone = errors.New("warp-plus завершился до готовности")

// wrongExit — туннель Psiphon поднялся, но проверка выхода не прошла:
// страна не совпала с запрошенной, выход RU или геосервисы промолчали.
// Причина-обёртка (errWrongCountry/errExitRU/errNoGeoService) сохраняется в
// цепочке ошибок, а got — страна, которую реально отдал выход.
type wrongExit struct {
	err error
	got string
}

func (w *wrongExit) Error() string {
	if w.got != "" {
		return fmt.Sprintf("%v (выход %s)", w.err, w.got)
	}
	return w.err.Error()
}

func (w *wrongExit) Unwrap() error { return w.err }

// waitReady опрашивает геосервисы через SOCKS5 до готовности или таймаута.
//
// Последний результат проверки не выбрасывается: если Psiphon поднялся, а выход
// не тот, именно эта причина и нужна в отчёте — иначе несовпадение страны
// выглядело бы как поломка туннеля.
func waitReady(ctx context.Context, c *child, client *http.Client, country string, opts Options, start time.Time) ([]GeoResult, error) {
	deadline := start.Add(opts.ReadyTimeout)
	var (
		lastResults []GeoResult
		lastErr     error
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		results, evalErr := Evaluate(ctx, client, DefaultGeoServices, country, opts.Strict)
		if evalErr == nil {
			return results, nil
		}
		lastResults, lastErr = results, evalErr
		// Молчание геосервисов — вердикт только если SOCKS5 уже принимает
		// соединения: иначе это «прокси ещё не поднялся», и точнее будет лог.
		reachable := evalErr != nil && errors.Is(evalErr, errNoGeoService) && proxyReachable(opts.Bind)

		if !c.Alive() {
			// Процесс мог успеть поднять туннель и выйти: если проверка выхода уже
			// дала вердикт, он точнее разбора лога.
			if cause := exitCause(lastErr, lastResults, reachable); cause != nil {
				return lastResults, cause
			}
			return nil, fmt.Errorf("%w: %s", errProcessGone, classifyLog(c.log()))
		}
		if time.Now().After(deadline) {
			if cause := exitCause(lastErr, lastResults, reachable); cause != nil {
				return lastResults, cause
			}
			return nil, fmt.Errorf("готовность не достигнута за %s", opts.ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// exitCause превращает ошибку проверки выхода в *wrongExit (nil — проверки ещё
// не было или туннеля нет вовсе). reachable — SOCKS5 warp-plus уже принимает
// соединения: без этого «геосервисы промолчали» означает не блокировку, а то, что
// прокси ещё не поднялся.
func exitCause(err error, results []GeoResult, reachable bool) *wrongExit {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errWrongCountry), errors.Is(err, errExitRU):
		return &wrongExit{err: err, got: observedCountry(results)}
	case errors.Is(err, errNoGeoService):
		if !reachable {
			return nil
		}
		return &wrongExit{err: err}
	}
	return nil
}

// proxyReachable — SOCKS5 warp-plus уже слушает --bind.
func proxyReachable(bind string) bool {
	conn, err := net.DialTimeout("tcp", bind, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// observedCountry — страна, которую отдал выход (первый ответивший геосервис).
func observedCountry(results []GeoResult) string {
	for _, r := range results {
		if r.Country != "" {
			return r.Country
		}
	}
	return ""
}

// readinessReason переводит ошибку готовности в метку для таблицы попыток.
func readinessReason(err error, logContent string) string {
	var we *wrongExit
	if errors.As(err, &we) {
		switch {
		case errors.Is(err, errExitRU):
			return "не та страна (выход RU)"
		case errors.Is(err, errWrongCountry):
			if we.got != "" {
				return "не та страна: выход " + we.got
			}
			return "не та страна"
		case errors.Is(err, errNoGeoService):
			return "геосервисы не ответили — выход не проверен"
		}
	}
	return classifyLog(logContent)
}

// describeFailure — строка причины для таблицы попыток: сначала точный вердикт
// проверки выхода, потом звено по логу warp-plus. К причине Psiphon-этапа
// добавляется хвост его notice'ов — по ним видно, докуда дошёл клиент.
func describeFailure(err error, logContent string) string {
	reason := readinessReason(err, logContent)
	switch {
	case strings.HasPrefix(reason, "не та страна"),
		strings.HasPrefix(reason, "геосервисы"):
		return reason
	}
	if notices := psiphonNotices(logContent, 3); len(notices) > 0 {
		return reason + " (" + strings.Join(notices, " → ") + ")"
	}
	return reason
}

// classifyLog по логу warp-plus определяет звено, на котором сорвалась попытка.
// Сам лог на экран не выводится — в нём могут быть служебные строки.
func classifyLog(content string) string {
	switch {
	case content == "":
		return "нет данных (лог пуст)"
	case strings.Contains(content, "failed to open data store"):
		return "Psiphon: кэш не открылся"
	case strings.Contains(content, "psiphon.NewController failed"):
		return "Psiphon: конфигурация отклонена"
	case strings.Contains(content, "tunnel establishment timeout"):
		return "Psiphon не построил туннель"
	case strings.Contains(content, "unable to run psiphon"):
		return "Psiphon не запустился"
	case strings.Contains(content, "connection test failed"):
		return "гейт warp-plus (--test-url)"
	case strings.Contains(content, "waiting on handshake"):
		return "нет хендшейка WARP"
	default:
		return "готовность не достигнута"
	}
}

// psiphonNotices собирает типы последних notice'ов Psiphon из лога warp-plus
// (`-v` пишет их строками `msg="psiphon core notice" type=...`).
func psiphonNotices(content string, limit int) []string {
	var all []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "psiphon core notice") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if name, ok := strings.CutPrefix(field, "type="); ok && name != "" {
				if len(all) == 0 || all[len(all)-1] != name {
					all = append(all, name)
				}
				break
			}
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
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
// На Windows рядом лежит warp-plus.exe: имя без расширения там не открыть.
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
		dir := filepath.Dir(self)
		names := []string{"warp-plus"}
		if runtime.GOOS == "windows" {
			names = append(names, "warp-plus.exe")
		}
		for _, name := range names {
			cand := filepath.Join(dir, name)
			if err := checkExecutable(cand); err == nil {
				return cand, nil
			}
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
