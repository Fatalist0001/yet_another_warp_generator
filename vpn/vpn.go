// Package vpn — системный туннель: весь трафик машины идёт по той же цепочке,
// которую команда run отдаёт наружу через SOCKS5.
//
// Схема: TUN → мост на gVisor netstack → SOCKS5 (Psiphon) → внутренний WARP →
// внешний AmneziaWG → Cloudflare. Мост принимает TCP-потоки к любым адресам и
// открывает под каждый SOCKS5 CONNECT; DNS перехватывается на 53-й порт (UDP и
// TCP) и уходит резолверу как DNS-over-TCP через SOCKS5. Остальной UDP, ICMP и
// IPv6 не проводятся, но и не пропадают молча: на них приходит ошибка ICMP
// («порт недоступен» / «нет маршрута»), чтобы приложение сразу откатывалось на
// TCP или IPv4, а не ждало собственного таймаута (см. Bridge, icmp.go).
//
// Пакет платформенный: устройство пакетов, маршруты и проверка прав лежат в
// файлах с build tags. Всё, что можно проверить офлайн — мост, политика пакетов,
// выбор DNS, запись и откат команд, — отделено от системных вызовов и
// тестируется без root, TUN и сети.
package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Значения по умолчанию.
const (
	// DefaultTUNName — имя TUN-интерфейса.
	DefaultTUNName = "warpgen0"
	// DefaultTUNMTU — MTU TUN: меньше, чем у обычного интерфейса, чтобы пакеты
	// заведомо проходили через SOCKS5 и цепочку.
	DefaultTUNMTU = 1400
	// DefaultTUNAddr / DefaultTUNPrefix — адрес TUN из диапазона 198.18.0.0/15
	// (зарезервирован под тесты производительности, в домашних сетях не
	// встречается). Системный DNS перенаправляется на соседний адрес подсети
	// (198.18.0.2): так запросы уходят в устройство, а не разбираются ядром
	// локально, и попадают в наш стек, откуда идут резолверу через SOCKS5.
	DefaultTUNAddr   = "198.18.0.1"
	DefaultTUNPrefix = 15
	// DefaultDNS — резолверы-кандидаты (ip[:port]).
	DefaultDNS = "1.1.1.1:53,8.8.8.8:53"
	// DefaultDialTimeout — таймаут SOCKS5-соединения.
	DefaultDialTimeout = 10 * time.Second
	// DefaultIdleTimeout — простой соединения, после которого оно закрывается.
	DefaultIdleTimeout = 90 * time.Second
	// DefaultDNSTimeout — таймаут проверки и обмена по DNS.
	DefaultDNSTimeout = 3 * time.Second
	// DefaultStopTimeout — предел на откат маршрутов при остановке.
	DefaultStopTimeout = 15 * time.Second

	// maxInFlight — сколько TCP-соединений мост держит в стадии установки.
	maxInFlight = 1024
	// maxPacket — размер буфера под один пакет TUN.
	maxPacket = 64 << 10
	// dnsBuffer — размер буфера под один DNS-датаграм.
	dnsBuffer = 4096
	// maxPumpFails — сколько подряд сбоев чтения или записи TUN терпит мост,
	// прежде чем остановить насос (живое устройство столько не даёт; один
	// сбойный пакет не должен гасить весь системный туннель).
	maxPumpFails = 100
	// maxRejectPerSecond — предел ICMP-отказов в секунду: пачка мёртвых
	// соединений не должна занимать насос устройства.
	maxRejectPerSecond = 200
)

// ErrUnsupported — платформа не поддерживается (TUN и маршруты реализованы для
// Linux и Windows, на Android TUN даёт VpnService; для остальных ОС — заглушки).
var ErrUnsupported = errors.New("vpn: не поддерживается на этой ОС")

// DialFunc — дозвон через SOCKS5 (в vpn — dialSocks5; в тестах — своя подделка).
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DialContext позволяет использовать DialFunc там, где нужен интерфейс
// дозвонщика (например, проверка DNS в dnscheck).
func (f DialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

// UDPPolicy — что делать с UDP, который не является DNS.
type UDPPolicy string

const (
	// UDPDrop — не проводить (значение по умолчанию): через SOCKS5 доступен
	// только TCP, поэтому QUIC (UDP/443) не проксируется, а в ответ уходит
	// ICMPv4 «порт недоступен» — приложение откатывается на TCP сразу, а не
	// после собственного таймаута. Проброс UDP (игры, VoIP) пока не
	// реализован — точка расширения — в Bridge.handleUDP.
	UDPDrop UDPPolicy = "drop"
)

// ParseUDPPolicy разбирает значение флага --udp.
func ParseUDPPolicy(s string) (UDPPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(UDPDrop):
		return UDPDrop, nil
	}
	return "", fmt.Errorf("vpn: неизвестная политика UDP %q: поддерживается только %q (проброс UDP не реализован)", s, UDPDrop)
}

// Options — параметры системного туннеля.
type Options struct {
	// TUNName — имя TUN-интерфейса.
	TUNName string
	// TUNAddr / TUNPrefix — адрес и маска TUN. Системный DNS отдаётся на
	// следующий адрес подсети (см. plan): адрес самого интерфейса ядро
	// доставляет локально мимо моста.
	TUNAddr   string
	TUNPrefix int
	// MTU — MTU TUN.
	MTU int
	// DNS — резолверы-кандидаты (ip[:port] через цепочку) в порядке предпочтения.
	// Именно их проверяет выбор резолвера; системному DNS отдаётся адрес TUN.
	DNS []string
	// UDP — политика обработки UDP (пусто — UDPDrop).
	UDP UDPPolicy
	// DryRun — напечатать команды и ничего не менять в системе.
	DryRun bool

	// DialTimeout — таймаут SOCKS5-соединения.
	DialTimeout time.Duration
	// IdleTimeout — простой соединения, после которого оно закрывается.
	IdleTimeout time.Duration
	// DNSTimeout — таймаут проверки резолвера и одного DNS-обмена.
	DNSTimeout time.Duration
	// StopTimeout — предел на откат маршрутов.
	StopTimeout time.Duration

	// Device — готовое устройство пакетов (nil — создаём сами через пакет tun).
	// Точка адаптации под Android: fd даёт VpnService.Builder, см. DeviceFromFile.
	Device Device
	// Protect — точка адаптации под Android: VpnService.protect(fd) уводит сокет
	// мимо VPN, чтобы наши же SOCKS5-соединения не ушли в собственный TUN.
	// На Linux и десктопах не нужна (nil): петля исключена маршрутом /32.
	Protect func(fd int) error
	// Executor — исполнитель системных команд (nil — реальный).
	Executor Executor
	// CheckPrivileges — проверка прав (nil — платформенная: на Linux нужен root).
	// Точка адаптации: на Android права даёт VpnService, проверка не нужна.
	CheckPrivileges func() error
	// StateDir — каталог файлов состояния маршрутов для аварийного отката
	// (пусто — платформенный каталог состояния; в тестах — подкаталог).
	StateDir string
	// Force — откат без журнала (`tun-revert --force`): снять только то, что
	// можно уверенно опознать как наше, — DNS из подсети TUN на физических
	// интерфейсах и маршруты через warpgen-адаптер. Нужен, когда журнал
	// потерян (портативную папку перенесли) или его не было.
	Force bool
	// Logf — приёмник служебных сообщений (nil — молча).
	Logf func(format string, args ...any)
}

func (o Options) withDefaults() Options {
	if o.TUNName == "" {
		o.TUNName = DefaultTUNName
	}
	if o.TUNAddr == "" {
		o.TUNAddr = DefaultTUNAddr
	}
	if o.TUNPrefix == 0 {
		o.TUNPrefix = DefaultTUNPrefix
	}
	if o.MTU == 0 {
		o.MTU = DefaultTUNMTU
	}
	if len(o.DNS) == 0 {
		o.DNS = strings.Split(DefaultDNS, ",")
	}
	if o.UDP == "" {
		o.UDP = UDPDrop
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.DNSTimeout <= 0 {
		o.DNSTimeout = DefaultDNSTimeout
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = DefaultStopTimeout
	}
	return o
}

func (o Options) validate() error {
	if _, err := ParseUDPPolicy(string(o.UDP)); err != nil {
		return err
	}
	if _, err := o.addr(); err != nil {
		return err
	}
	if o.TUNName == "" || len(o.TUNName) >= 16 {
		return fmt.Errorf("vpn: имя интерфейса %q не влезает в лимит (до 15 символов)", o.TUNName)
	}
	if o.MTU < 576 || o.MTU > 65535 {
		return fmt.Errorf("vpn: MTU %d вне разумных пределов", o.MTU)
	}
	if o.TUNPrefix < 8 || o.TUNPrefix > 30 {
		return fmt.Errorf("vpn: маска TUN /%d вне разумных пределов", o.TUNPrefix)
	}
	return nil
}

// addr — адрес TUN.
func (o Options) addr() (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(o.TUNAddr))
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("vpn: адрес TUN %q должен быть IPv4-адресом", o.TUNAddr)
	}
	return a, nil
}

// Status — что удалось сделать системному туннелю.
type Status struct {
	// Name / MTU — поднятый TUN-интерфейс ("" — не поднимался).
	Name string
	MTU  int
	// Routes — сколько системных команд применил RouteManager (в dry-run —
	// сколько применил бы).
	Routes int
	// DNS / DNSRTT — выбранный резолвер и замер задержки.
	DNS    string
	DNSRTT time.Duration
	// DNSErr — почему резолвер не выбран или системный DNS не перенастроен
	// (пусто — всё в порядке).
	DNSErr string
	// Warnings — не критичные проблемы (например, нет утилиты resolvectl).
	Warnings []string
	// DryRun — ничего не выполнялось, только напечатано.
	DryRun bool
}

// SystemTunnel — системный туннель целиком: TUN, мост, маршруты и DNS.
type SystemTunnel struct {
	opts   Options
	routes RouteManager

	bridge   *Bridge
	dev      Device
	started  bool
	endpoint string

	socks    atomic.Value // string
	resolver *Resolver

	mu sync.Mutex
}

// CheckPrivileges проверяет права, нужные режиму --tun, не выполняя системных
// действий. Нужна CLI до резолва DNS-имён: без root команда обязана упасть
// раньше, чем пойдёт в сеть. На Android проверка подменяется через
// Options.CheckPrivileges, эта функция — платформенная.
func CheckPrivileges() error {
	return checkPrivileges()
}

// New собирает системный туннель. Всё, что можно проверить без системных
// действий, проверяется сразу: имя интерфейса, адрес, маска, MTU, политика UDP,
// список резолверов и права на изменение маршрутов.
func New(opts Options) (*SystemTunnel, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if !opts.DryRun {
		check := opts.CheckPrivileges
		if check == nil {
			check = checkPrivileges
		}
		if err := check(); err != nil {
			return nil, err
		}
	}

	t := &SystemTunnel{opts: opts}
	t.socks.Store("")

	routes, err := newRouteManager(opts)
	if err != nil {
		return nil, err
	}
	t.routes = routes

	resolver, err := NewResolver(opts.DNS, t.dial, opts.DNSTimeout, opts.Logf)
	if err != nil {
		return nil, err
	}
	t.resolver = resolver
	return t, nil
}

// Start поднимает TUN и мост на готовый SOCKS5, применяет маршруты и выбирает
// DNS-резолвер. Порядок ровно такой: маршруты нельзя применять раньше моста,
// иначе трафик уйдёт в никуда. Любая ошибка откатывает уже применённое.
func (t *SystemTunnel) Start(ctx context.Context, socks, endpoint string) (Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.started {
		return Status{}, errors.New("vpn: системный туннель уже запущен")
	}
	t.socks.Store(socks)
	t.endpoint = endpoint

	st := Status{Name: t.opts.TUNName, MTU: t.opts.MTU, DryRun: t.opts.DryRun}
	plan, err := t.plan(endpoint)
	if err != nil {
		return st, err
	}

	if t.opts.DryRun {
		t.log("dry-run: TUN %s (%s/%d, MTU %d) не создаём, систему не меняем",
			t.opts.TUNName, t.opts.TUNAddr, t.opts.TUNPrefix, t.opts.MTU)
		res, err := t.routes.Apply(ctx, plan)
		if err != nil {
			return st, err
		}
		st.Routes, st.Warnings, st.DNSErr = res.Commands, res.Warnings, res.DNSWarn
		return st, nil
	}

	dev := t.opts.Device
	if dev == nil {
		dev, err = newDevice(t.opts.TUNName, t.opts.MTU)
		if err != nil {
			return st, err
		}
	}
	bridge, err := NewBridge(BridgeOptions{
		Device:      dev,
		TUNAddr:     plan.TUNAddr,
		MTU:         t.opts.MTU,
		Dial:        t.dial,
		DialTimeout: t.opts.DialTimeout,
		IdleTimeout: t.opts.IdleTimeout,
		Resolver:    t.resolver.Effective,
		Resolvers:   t.resolver.Candidates,
		UDP:         t.opts.UDP,
		Logf:        t.opts.Logf,
	})
	if err != nil {
		_ = dev.Close()
		return st, err
	}
	bridge.Start(ctx)
	st.Name, st.MTU = dev.Name(), dev.MTU()

	res, aerr := t.routes.Apply(ctx, plan)
	if aerr != nil {
		bridge.Close()
		_ = dev.Close()
		return st, aerr
	}
	st.Routes, st.Warnings, st.DNSErr = res.Commands, res.Warnings, res.DNSWarn

	t.dev, t.bridge, t.started = dev, bridge, true
	t.selectDNS(ctx, &st)
	return st, nil
}

// UpdateSocks переводит мост на новый SOCKS5 после смены цепочки и заново
// выбирает резолвер. Маршруты и TUN не трогает: на время смены звена новые
// соединения падают, но трафик не уходит напрямую (kill switch).
func (t *SystemTunnel) UpdateSocks(ctx context.Context, socks string) (Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.started && !t.opts.DryRun {
		return Status{}, errors.New("vpn: системный туннель не запущен")
	}
	t.socks.Store(socks)

	st := Status{Name: t.opts.TUNName, MTU: t.opts.MTU, DryRun: t.opts.DryRun}
	if t.dev != nil {
		st.Name = t.dev.Name()
	}
	if t.opts.DryRun {
		return st, nil
	}
	t.selectDNS(ctx, &st)
	return st, nil
}

// UpdateEndpoint обновляет исключение для нового внешнего endpoint: его /32
// должен идти мимо TUN, иначе UDP-сокет внешнего туннеля уйдёт в наш же TUN и
// цепочка зациклится.
func (t *SystemTunnel) UpdateEndpoint(ctx context.Context, endpoint string) (Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if endpoint == t.endpoint {
		return Status{Name: t.opts.TUNName, DryRun: t.opts.DryRun}, nil
	}
	ip, err := endpointIP(endpoint)
	if err != nil {
		return Status{}, err
	}
	if err := t.routes.SetEndpoint(ctx, ip); err != nil {
		return Status{}, err
	}
	t.endpoint = endpoint
	return Status{Name: t.opts.TUNName, DryRun: t.opts.DryRun}, nil
}

// Stop снимает маршруты и DNS, затем останавливает мост и TUN. Откат не зависит
// от отмены контекста: Ctrl+C уже отменил его, а маршруты снять всё равно надо.
// Повторный вызов безопасен.
func (t *SystemTunnel) Stop(ctx context.Context) error {
	t.mu.Lock()
	dev, bridge := t.dev, t.bridge
	t.dev, t.bridge, t.started = nil, nil, false
	t.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.opts.StopTimeout)
	defer cancel()

	var errs []error
	if err := t.routes.Revert(ctx); err != nil {
		errs = append(errs, err)
	}
	if bridge != nil {
		if err := bridge.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if dev != nil {
		if err := dev.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Stats — счётчики моста (после остановки — итоговые).
func (t *SystemTunnel) Stats() BridgeStats {
	t.mu.Lock()
	bridge := t.bridge
	t.mu.Unlock()
	if bridge == nil {
		return BridgeStats{}
	}
	return bridge.Stats()
}

// plan собирает план для RouteManager.
func (t *SystemTunnel) plan(endpoint string) (Plan, error) {
	addr, err := t.opts.addr()
	if err != nil {
		return Plan{}, err
	}
	ip, err := endpointIP(endpoint)
	if err != nil {
		return Plan{}, err
	}
	// Системный DNS указывает на соседний адрес подсети TUN, а не на адрес
	// интерфейса: пакет к собственному адресу ядро доставляет локально (адрес
	// попадает в таблицу local) и до моста не доходит — запросы виснут или
	// получают отказ. Любой другой адрес подсети уходит в устройство, и мост
	// перехватывает его как DNS.
	dnsAddr := addr.Next()
	return Plan{
		TUNName:   t.opts.TUNName,
		TUNAddr:   addr,
		TUNPrefix: t.opts.TUNPrefix,
		MTU:       t.opts.MTU,
		Exclude:   []netip.Addr{ip},
		DNS:       dnsAddr,
	}, nil
}

// selectDNS перепроверяет резолверы через текущую цепочку.
func (t *SystemTunnel) selectDNS(ctx context.Context, st *Status) {
	if t.opts.DryRun {
		return
	}
	addr, rtt, err := t.resolver.Select(ctx)
	if err != nil {
		st.DNSErr = joinReason(st.DNSErr, err.Error())
		return
	}
	st.DNS, st.DNSRTT = addr, rtt
}

// dial открывает соединение через SOCKS5 текущей цепочки. Адрес берётся на
// момент соединения, поэтому смена цепочки не рвёт уже поднятый мост. UDP через
// SOCKS5 не поддерживается: проброс UDP в проекте не реализован.
func (t *SystemTunnel) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("vpn: через SOCKS5 доступен только TCP, а не %q", network)
	}
	socks, _ := t.socks.Load().(string)
	if strings.TrimSpace(socks) == "" {
		return nil, errors.New("vpn: SOCKS5 ещё не готов")
	}
	return dialSocks5(ctx, socks, address, t.opts.Protect)
}

// endpointIP разбирает адрес внешнего endpoint (ip:port).
func endpointIP(endpoint string) (netip.Addr, error) {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(endpoint))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("vpn: адрес endpoint %q не разбирается: %w", endpoint, err)
	}
	if !ap.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("vpn: внешний endpoint %q должен быть IPv4", endpoint)
	}
	return ap.Addr(), nil
}

// joinReason склеивает несколько причин в одну строку без повторов.
func joinReason(parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if !contains(out, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (t *SystemTunnel) log(format string, args ...any) {
	if t.opts.Logf != nil {
		t.opts.Logf(format, args...)
	}
}
