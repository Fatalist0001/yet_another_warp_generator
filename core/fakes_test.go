package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// --- поддельный туннель ---

// tunnelFleet управляет поддельными туннелями теста: выдаёт их фабрикой, считает
// созданные/закрытые и подменяет ответы хендшейка и trace.
//
// Соединения внутри туннеля уходят на локальное UDP-эхо (loopback), поэтому
// форвардер проверяется без выхода в сеть.
type tunnelFleet struct {
	// echo — адрес локального UDP-эха ("" — DialContext отдаёт ошибку).
	echo string
	// tcpEcho — адрес локального TCP-эха для режима Direct ("" — отказ).
	tcpEcho string
	// HandshakeFn — ответ хендшейка по номеру вызова (nil — успех).
	HandshakeFn func(call int, endpoint string) bool
	// TraceFn — ответ trace по номеру вызова (nil — успешный trace warp=on).
	TraceFn func(call int) (tunnel.TraceResult, error)

	mu             sync.Mutex
	created        int
	closed         int
	handshakeCalls int
	traceCalls     int
}

func (f *tunnelFleet) factory() TunnelFactory {
	return func() (Tunnel, error) {
		f.mu.Lock()
		f.created++
		f.mu.Unlock()
		return &fakeTunnel{fleet: f}, nil
	}
}

// counts — созданные и закрытые туннели.
func (f *tunnelFleet) counts() (created, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.closed
}

func (f *tunnelFleet) calls() (handshakes, traces int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handshakeCalls, f.traceCalls
}

type fakeTunnel struct {
	fleet *tunnelFleet
}

func (t *fakeTunnel) Handshake(_ context.Context, endpoint string, _ time.Duration) bool {
	t.fleet.mu.Lock()
	t.fleet.handshakeCalls++
	call := t.fleet.handshakeCalls
	fn := t.fleet.HandshakeFn
	t.fleet.mu.Unlock()
	if fn == nil {
		return true
	}
	return fn(call, endpoint)
}

func (t *fakeTunnel) FetchTrace(context.Context, time.Duration) (tunnel.TraceResult, error) {
	t.fleet.mu.Lock()
	t.fleet.traceCalls++
	call := t.fleet.traceCalls
	fn := t.fleet.TraceFn
	t.fleet.mu.Unlock()
	if fn == nil {
		return traceOK(), nil
	}
	return fn(call)
}

func (t *fakeTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	if strings.HasPrefix(network, "tcp") {
		if t.fleet.tcpEcho == "" {
			return nil, errors.New("поддельный туннель: не задано TCP-эхо")
		}
		return d.DialContext(ctx, "tcp", t.fleet.tcpEcho)
	}
	if t.fleet.echo == "" {
		return nil, errors.New("поддельный туннель: не задано эхо")
	}
	return d.DialContext(ctx, "udp", t.fleet.echo)
}

func (t *fakeTunnel) Close() {
	t.fleet.mu.Lock()
	t.fleet.closed++
	t.fleet.mu.Unlock()
}

// traceOK — ответ trace изнутри здорового туннеля.
func traceOK() tunnel.TraceResult {
	return tunnel.ParseTrace("warp=on\ncolo=HEL\nloc=RU\nip=203.0.113.1\n")
}

// --- поддельный поиск endpoint ---

type fakeLocator struct {
	endpoints []Endpoint
	err       error

	mu    sync.Mutex
	calls int
}

func (l *fakeLocator) Locate(_ context.Context, limit int) ([]Endpoint, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	out := l.endpoints
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- поддельная цепочка WARP → Psiphon ---

// chainStep — ответ одной попытки страны.
type chainStep struct {
	// reason != "" — попытка страны провалилась с этой причиной.
	reason string
}

type fakeChain struct {
	// steps — очередь ответов по номеру вызова (последний повторяется).
	steps []chainStep
	// RunFn — свой сценарий вместо очереди.
	RunFn func(ctx context.Context, req ChainRequest) (*ChainSession, error)
	// socks — адрес, который выдаёт поддельный SOCKS5.
	socks string

	mu       sync.Mutex
	calls    int
	sessions int
	closed   int
}

func (c *fakeChain) Run(ctx context.Context, req ChainRequest) (*ChainSession, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	fn := c.RunFn
	var step chainStep
	if len(c.steps) > 0 {
		i := call - 1
		if i >= len(c.steps) {
			i = len(c.steps) - 1
		}
		step = c.steps[i]
	}
	c.mu.Unlock()

	if fn != nil {
		return fn(ctx, req)
	}
	if step.reason != "" {
		return nil, errors.New(step.reason)
	}

	c.mu.Lock()
	c.sessions++
	c.mu.Unlock()
	socks := c.socks
	if socks == "" {
		socks = "127.0.0.1:8086"
	}
	sess := &ChainSession{
		SOCKS:   socks,
		Country: req.Countries[0],
		IP:      "203.0.113.9",
		Org:     "AS123 Test",
		Elapsed: 42 * time.Second,
	}
	sess.Close = func() error {
		c.mu.Lock()
		c.closed++
		c.sessions--
		c.mu.Unlock()
		return nil
	}
	return sess, nil
}

// counts — сколько раз запускали цепочку, сколько сессий закрыто.
func (c *fakeChain) counts() (calls, closed, open int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.closed, c.sessions
}

// --- поддельная проверка выхода ---

// probeStep — ответ одной проверки через SOCKS5.
type probeStep struct {
	res ProbeResult
	err error
}

type fakeProber struct {
	// steps — очередь ответов по номеру вызова (последний повторяется).
	steps []probeStep

	mu    sync.Mutex
	calls int
}

func healthyProbe() ProbeResult {
	return ProbeResult{Country: "NL", IP: "203.0.113.9", Org: "AS123 Test", Services: 2}
}

// count — сколько раз опрашивали геосервисы.
func (p *fakeProber) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *fakeProber) Probe(_ context.Context, _ string) (ProbeResult, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	if len(p.steps) == 0 {
		return healthyProbe(), nil
	}
	i := call
	if i >= len(p.steps) {
		i = len(p.steps) - 1
	}
	return p.steps[i].res, p.steps[i].err
}

// --- поддельный системный туннель ---

// tunStep — одно обращение к системному туннелю (для проверки порядка).
type tunStep struct {
	// call — start / socks / endpoint / stop.
	call     string
	socks    string
	endpoint string
}

// fakeTUN — поддельный системный туннель: запоминает вызовы и подменяет статусы.
type fakeTUN struct {
	// StartErr / StopErr / SocksErr / EndpointErr — ошибки соответствующих вызовов.
	StartErr    error
	StopErr     error
	SocksErr    error
	EndpointErr error
	// Status — статус запуска (пустые поля заполняются разумными значениями).
	Status vpn.Status
	// FactoryErr — ошибка создания системного туннеля.
	FactoryErr error

	mu      sync.Mutex
	steps   []tunStep
	created int
	starts  int
	stops   int
}

func (f *fakeTUN) factory() func() (TUNSystem, error) {
	return func() (TUNSystem, error) {
		f.mu.Lock()
		f.created++
		f.mu.Unlock()
		if f.FactoryErr != nil {
			return nil, f.FactoryErr
		}
		return f, nil
	}
}

func (f *fakeTUN) record(step tunStep) {
	f.mu.Lock()
	f.steps = append(f.steps, step)
	f.mu.Unlock()
}

// steps — журнал вызовов.
func (f *fakeTUN) calls() []tunStep {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tunStep(nil), f.steps...)
}

// counts — сколько раз создавали, запускали и останавливали туннель.
func (f *fakeTUN) counts() (created, starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.starts, f.stops
}

// status — статус с заполненными по умолчанию полями.
func (f *fakeTUN) status() vpn.Status {
	st := f.Status
	if st.Name == "" {
		st.Name, st.MTU = "warpgen0", 1400
	}
	if st.Routes == 0 {
		st.Routes = 9
	}
	if st.DNS == "" && st.DNSErr == "" {
		st.DNS, st.DNSRTT = "1.1.1.1:53", 20*time.Millisecond
	}
	return st
}

func (f *fakeTUN) Start(_ context.Context, socks, endpoint string) (vpn.Status, error) {
	f.record(tunStep{call: "start", socks: socks, endpoint: endpoint})
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	if f.StartErr != nil {
		return vpn.Status{}, f.StartErr
	}
	return f.status(), nil
}

func (f *fakeTUN) UpdateSocks(_ context.Context, socks string) (vpn.Status, error) {
	f.record(tunStep{call: "socks", socks: socks})
	if f.SocksErr != nil {
		return vpn.Status{}, f.SocksErr
	}
	return f.status(), nil
}

func (f *fakeTUN) UpdateEndpoint(_ context.Context, endpoint string) (vpn.Status, error) {
	f.record(tunStep{call: "endpoint", endpoint: endpoint})
	if f.EndpointErr != nil {
		return vpn.Status{}, f.EndpointErr
	}
	return vpn.Status{Name: "warpgen0"}, nil
}

func (f *fakeTUN) Stop(context.Context) error {
	f.record(tunStep{call: "stop"})
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	return f.StopErr
}

// --- часы ---

// testClock — часы для тестов: таймеры и тикеры срабатывают с минимальной
// задержкой независимо от запрошенного интервала, поэтому тест идёт миллисекунды.
type testClock struct{ tick time.Duration }

func (c testClock) Now() time.Time { return time.Now() }

func (c testClock) After(time.Duration) <-chan time.Time {
	return time.After(c.tick)
}

func (c testClock) Ticker(time.Duration) (<-chan time.Time, func()) {
	stop := make(chan struct{})
	ch := make(chan time.Time, 1)
	go func() {
		t := time.NewTicker(c.tick)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case ts := <-t.C:
				select {
				case ch <- ts:
				default:
				}
			}
		}
	}()
	var once sync.Once
	return ch, func() { once.Do(func() { close(stop) }) }
}

// --- helpers ---

// udpEcho поднимает локальный UDP-эхо-сервер (только loopback) и возвращает адрес.
func udpEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("эхо-сервер: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	go func() {
		buf := make([]byte, maxPacketSize)
		for {
			n, src, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], src)
		}
	}()
	return pc.LocalAddr().String()
}

// testConfig — конфигурация пайплайна на подделках с быстрыми часами.
func testConfig(fleet *tunnelFleet, loc *fakeLocator, ch *fakeChain, pr *fakeProber) Config {
	return Config{
		Tunnel:          fleet.factory(),
		Locator:         loc,
		Chain:           ch,
		Prober:          pr,
		Clock:           testClock{tick: time.Millisecond},
		Countries:       []string{"NL"},
		InnerEndpoint:   "162.159.192.1:2408",
		Listen:          "127.0.0.1:0",
		Endpoints:       1,
		MonitorInterval: DefaultMonitorInterval,
	}
}

// expectEvent читает следующее событие и сверяет его тип.
func expectEvent(t *testing.T, events <-chan Event, want Kind, where string) Event {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatalf("%s: поток событий закрылся, ждали %s", where, want)
		}
		if ev.Kind != want {
			t.Fatalf("%s: событие %s вместо %s (reason=%q, err=%q)", where, ev.Kind, want, ev.Reason, ev.Err)
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: не дождались события %s", where, want)
		return Event{}
	}
}

// nextUntil читает события, пока не встретит нужное.
func nextUntil(t *testing.T, events <-chan Event, want Kind, where string) Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("%s: поток событий закрылся, ждали %s", where, want)
			}
			if ev.Kind == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("%s: не дождались события %s", where, want)
			return Event{}
		}
	}
}

// drain читает события до закрытия потока.
func drain(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("поток событий не закрылся, уже получено %d событий", len(out))
			return out
		}
	}
}

// closed проверяет, что поток событий закрыт.
func closedChannel(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case _, ok := <-events:
		if ok {
			t.Error("в потоке есть лишние события после stopped")
		}
	case <-time.After(5 * time.Second):
		t.Error("поток событий не закрылся после stopped")
	}
}
