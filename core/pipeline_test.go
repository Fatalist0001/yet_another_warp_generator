package core

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

// endpoint162 — кандидат по умолчанию для тестов.
func endpoint162(addr string) Endpoint {
	return Endpoint{Addr: addr, Colo: "HEL", Loc: "RU", RTT: 84 * time.Millisecond}
}

// startRun запускает пайплайн и возвращает поток событий и отмену.
func startRun(t *testing.T, cfg Config) (<-chan Event, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events, err := Run(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("Run: %v", err)
	}
	return events, cancel
}

// happyPath — конфигурация, которая доходит до ready с первой попытки.
func happyPath(t *testing.T) (*tunnelFleet, *fakeLocator, *fakeChain, *fakeProber) {
	t.Helper()
	fleet := &tunnelFleet{}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	return fleet, loc, &fakeChain{}, &fakeProber{}
}

// Проверяем порядок событий happy path и чистую остановку по ctx.
func TestRunEmitsReadyFlow(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	events, cancel := startRun(t, testConfig(fleet, loc, ch, pr))

	ev := expectEvent(t, events, EventEndpointSelected, "старт")
	if ev.Endpoint != "162.159.192.1:2408" || ev.Colo != "HEL" || ev.RTTMS != 84 {
		t.Errorf("данные endpoint неполные: %+v", ev)
	}
	expectEvent(t, events, EventOuterUp, "после выбора endpoint")
	expectEvent(t, events, EventOuterChecked, "после хендшейка")
	if fwd := expectEvent(t, events, EventForwarderUp, "после проверки туннеля"); !strings.HasPrefix(fwd.Forwarder, "127.0.0.1:") {
		t.Errorf("адрес форвардера = %q, ожидали loopback", fwd.Forwarder)
	}
	if ps := expectEvent(t, events, EventPsiphonStarting, "перед цепочкой"); ps.Country != "NL" {
		t.Errorf("страна попытки = %q, ожидали NL", ps.Country)
	}
	ready := expectEvent(t, events, EventReady, "готовность")
	if ready.SOCKS != "127.0.0.1:8086" || ready.Country != "NL" || ready.IP != "203.0.113.9" {
		t.Errorf("ready = %+v", ready)
	}
	if ready.Seconds != 42 {
		t.Errorf("подготовка = %v сек, ожидали 42", ready.Seconds)
	}

	cancel()
	evs := drain(t, events)
	if len(evs) != 1 || evs[0].Kind != EventStopped || evs[0].Err != "" {
		t.Fatalf("остановка по ctx: %+v", evs)
	}
	closedChannel(t, events)

	// Ресурсы освобождены: сессии цепочки закрыты, туннель закрыт.
	if calls, closed, open := ch.counts(); calls != 1 || closed != 1 || open != 0 {
		t.Errorf("цепочка: вызовов %d, закрыто %d, живо %d", calls, closed, open)
	}
	if created, closed := fleet.counts(); created != 1 || closed != 1 {
		t.Errorf("туннели: создано %d, закрыто %d", created, closed)
	}
}

// Провал страны — берём следующую страну того же endpoint'а.
func TestRunFailsOverToNextCountry(t *testing.T) {
	fleet, loc, _, pr := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{reason: "Psiphon не построил туннель"}, {}}}
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	// Первая попытка: страна NL падает, затем пробуем SE.
	if ps := nextUntil(t, events, EventPsiphonStarting, "первая страна"); ps.Country != "NL" {
		t.Errorf("первая страна = %q, ожидали NL", ps.Country)
	}
	fail := expectEvent(t, events, EventFailover, "провал страны NL")
	if !strings.Contains(fail.Reason, "страна NL") || !strings.Contains(fail.Reason, "Psiphon") {
		t.Errorf("причина смены страны = %q", fail.Reason)
	}
	if ps := expectEvent(t, events, EventPsiphonStarting, "вторая страна"); ps.Country != "SE" {
		t.Errorf("вторая страна = %q, ожидали SE", ps.Country)
	}
	ready := expectEvent(t, events, EventReady, "готовность по второй стране")
	if ready.Country != "SE" {
		t.Errorf("успешная страна = %q, ожидали SE", ready.Country)
	}

	cancel()
	evs := drain(t, events)
	if last := evs[len(evs)-1]; last.Kind != EventStopped || last.Err != "" {
		t.Errorf("остановка: %+v", last)
	}
}

// Мёртвый endpoint — переходим к следующему кандидату.
func TestRunTriesNextEndpointOnHandshakeFailure(t *testing.T) {
	fleet := &tunnelFleet{HandshakeFn: func(call int, _ string) bool { return call != 1 }}
	loc := &fakeLocator{endpoints: []Endpoint{
		endpoint162("162.159.192.1:2408"),
		endpoint162("162.159.193.5:500"),
	}}
	cfg := testConfig(fleet, loc, &fakeChain{}, &fakeProber{})
	cfg.Endpoints = 2
	events, cancel := startRun(t, cfg)
	defer cancel()

	first := expectEvent(t, events, EventEndpointSelected, "первый кандидат")
	if first.Endpoint != "162.159.192.1:2408" {
		t.Errorf("первый endpoint = %q", first.Endpoint)
	}
	fail := expectEvent(t, events, EventFailover, "провал хендшейка")
	if !strings.Contains(fail.Reason, "хендшейк") {
		t.Errorf("причина = %q, ожидали упоминание хендшейка", fail.Reason)
	}
	second := expectEvent(t, events, EventEndpointSelected, "второй кандидат")
	if second.Endpoint != "162.159.193.5:500" {
		t.Errorf("второй endpoint = %q", second.Endpoint)
	}
	nextUntil(t, events, EventReady, "готовность на втором endpoint")

	cancel()
	evs := drain(t, events)
	if last := evs[len(evs)-1]; last.Kind != EventStopped || last.Err != "" {
		t.Errorf("последнее событие = %+v", last)
	}
}

// Все кандидаты мертвы — пайплайн останавливается с ошибкой.
func TestRunStopsWhenAllEndpointsFail(t *testing.T) {
	fleet := &tunnelFleet{HandshakeFn: func(int, string) bool { return false }}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408"), endpoint162("162.159.193.5:500")}}
	ch := &fakeChain{}
	cfg := testConfig(fleet, loc, ch, &fakeProber{})
	cfg.Endpoints = 2
	events, cancel := startRun(t, cfg)
	defer cancel()

	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || last.Err == "" {
		t.Fatalf("ожидали stopped с ошибкой, получили %+v", last)
	}
	if !strings.Contains(last.Err, "ни на одном из 2 endpoint") {
		t.Errorf("текст ошибки = %q", last.Err)
	}
	if calls, closed, _ := ch.counts(); calls != 0 || closed != 0 {
		t.Errorf("цепочка не должна запускаться, если туннель не поднялся: вызовов %d, закрытий %d", calls, closed)
	}
	if created, closed := fleet.counts(); created != 2 || closed != 2 {
		t.Errorf("туннели: создано %d, закрыто %d — оба должны быть закрыты", created, closed)
	}
}

// RU на выходе через SOCKS5 — сразу меняем страну.
func TestRunMonitorFailsOverOnRU(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	pr := &fakeProber{steps: []probeStep{
		{res: ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}},
		{res: healthyProbe()},
	}}
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	ready := nextUntil(t, events, EventReady, "первая готовность")
	if ready.Country != "NL" {
		t.Errorf("первая страна = %q, ожидали NL", ready.Country)
	}
	fail := nextUntil(t, events, EventFailover, "выход RU")
	if !strings.Contains(fail.Reason, "RU") {
		t.Errorf("причина = %q, ожидали упоминание RU", fail.Reason)
	}
	next := nextUntil(t, events, EventReady, "готовность после смены страны")
	if next.Country != "SE" {
		t.Errorf("страна после смены = %q, ожидали SE", next.Country)
	}

	cancel()
	evs := drain(t, events)
	if last := evs[len(evs)-1]; last.Kind != EventStopped || last.Err != "" {
		t.Errorf("остановка: %+v", last)
	}
}

// Три сбоя мониторинга подряд — смена страны; первые два дают degraded.
func TestRunMonitorDegradedThenFailover(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	pr := &fakeProber{steps: []probeStep{
		{err: errors.New("нет ответа")},
		{err: errors.New("нет ответа")},
		{err: errors.New("нет ответа")},
		{res: healthyProbe()},
	}}
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	nextUntil(t, events, EventReady, "первая готовность")
	for want := 1; want <= 3; want++ {
		ev := nextUntil(t, events, EventDegraded, "просадка")
		if ev.Fails != want {
			t.Errorf("сбой номер = %d, ожидали %d", ev.Fails, want)
		}
	}
	fail := nextUntil(t, events, EventFailover, "смена страны по сбоям")
	if !strings.Contains(fail.Reason, "3 сбоя") {
		t.Errorf("причина = %q, ожидали «3 сбоя»", fail.Reason)
	}
	if next := nextUntil(t, events, EventReady, "готовность после сбоев"); next.Country != "SE" {
		t.Errorf("страна = %q, ожидали SE", next.Country)
	}

	cancel()
	drain(t, events)
}

// Внешний туннель перестал отвечать — пересоздаём его и форвардер на следующем endpoint.
func TestRunRecreatesOuterTunnel(t *testing.T) {
	fleet := &tunnelFleet{TraceFn: func(call int) (tunnel.TraceResult, error) {
		if call == 1 || call == 3 {
			return traceOK(), nil
		}
		return tunnel.TraceResult{}, errors.New("таймаут")
	}}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	events, cancel := startRun(t, testConfig(fleet, loc, ch, &fakeProber{}))
	defer cancel()

	nextUntil(t, events, EventReady, "первая готовность")
	fail := nextUntil(t, events, EventFailover, "внешний туннель умер")
	if !strings.Contains(fail.Reason, "внешний туннель") {
		t.Errorf("причина = %q, ожидали «внешний туннель»", fail.Reason)
	}
	expectEvent(t, events, EventEndpointSelected, "пересоздание туннеля")
	expectEvent(t, events, EventOuterUp, "новый туннель")
	expectEvent(t, events, EventOuterChecked, "проверка нового туннеля")
	expectEvent(t, events, EventForwarderUp, "новый форвардер")
	if ready := nextUntil(t, events, EventReady, "готовность после пересоздания"); ready.Country == "" {
		t.Errorf("ready без страны: %+v", ready)
	}

	cancel()
	drain(t, events)
	if created, closed := fleet.counts(); created != 2 || closed != 2 {
		t.Errorf("туннели: создано %d, закрыто %d — ожидали по 2", created, closed)
	}
}

// Пакеты стороннего клиента (warp-plus) проходят через форвардер в туннель и обратно.
func TestRunRelaysForwarderTraffic(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}

	echoed := make(chan error, 1)
	ch := &fakeChain{RunFn: func(_ context.Context, req ChainRequest) (*ChainSession, error) {
		conn, err := net.Dial("udp", req.Forwarder)
		if err != nil {
			echoed <- err
			return nil, errors.New("нет форвардера")
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("handshake")); err != nil {
			echoed <- err
			return nil, errors.New("запись не прошла")
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			echoed <- err
			return nil, errors.New("ответа нет")
		}
		echoed <- nil
		if string(buf[:n]) != "handshake" {
			t.Errorf("через форвардер вернулось %q", buf[:n])
		}
		return &ChainSession{SOCKS: "127.0.0.1:8086", Country: "NL"}, nil
	}}
	events, cancel := startRun(t, testConfig(fleet, loc, ch, &fakeProber{}))

	select {
	case err := <-echoed:
		if err != nil {
			t.Fatalf("трафик через форвардер: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("форвардер не дождался трафика")
	}
	nextUntil(t, events, EventReady, "готовность")

	cancel()
	evs := drain(t, events)
	if last := evs[len(evs)-1]; last.Kind != EventStopped {
		t.Errorf("остановка: %+v", last)
	}
}

// Останавливаемся только по ctx и без лишних горутин.
func TestRunLeavesNoGoroutines(t *testing.T) {
	base := runtime.NumGoroutine()

	fleet, loc, ch, pr := happyPath(t)
	events, cancel := startRun(t, testConfig(fleet, loc, ch, pr))
	nextUntil(t, events, EventReady, "готовность")
	cancel()
	drain(t, events)
	closedChannel(t, events)

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.Gosched()
		if n := runtime.NumGoroutine(); n <= base+2 {
			return
		} else if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("после остановки осталось %d горутин (было %d):\n%s", n, base, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Отменённый контекст — штатная остановка без ошибки и без запуска цепочки.
func TestRunStopsOnCancelledContext(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events, err := Run(ctx, testConfig(fleet, loc, ch, pr))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs := drain(t, events)
	if len(evs) != 1 || evs[0].Kind != EventStopped || evs[0].Err != "" {
		t.Fatalf("остановка: %+v", evs)
	}
	if calls, _, _ := ch.counts(); calls != 0 {
		t.Errorf("цепочка запускалась %d раз при отменённом контексте", calls)
	}
}

// Ошибка preflight возвращается из Run до старта пайплайна.
func TestRunPreflightError(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Preflight = func(context.Context) error { return errors.New("нет аккаунта warpgen") }

	events, err := Run(context.Background(), cfg)
	if err == nil {
		t.Fatalf("ожидали ошибку preflight, получили поток %v", events)
	}
	if !strings.Contains(err.Error(), "аккаунт") {
		t.Errorf("текст ошибки = %q", err)
	}
}

// Неполная конфигурация отвергается до старта.
func TestRunValidation(t *testing.T) {
	fleet := &tunnelFleet{}
	loc := &fakeLocator{}
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"нет стран", Config{Tunnel: fleet.factory(), Locator: loc, Chain: &fakeChain{}}, "стран"},
		{"нет туннеля", Config{Countries: []string{"NL"}, Locator: loc, Chain: &fakeChain{}}, "Tunnel"},
		{"нет поиска endpoint", Config{Countries: []string{"NL"}, Tunnel: fleet.factory(), Chain: &fakeChain{}}, "Locator"},
		{"нет цепочки", Config{Countries: []string{"NL"}, Tunnel: fleet.factory(), Locator: loc}, "Chain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events, err := Run(context.Background(), c.cfg)
			if err == nil {
				t.Fatalf("ожидали ошибку конфигурации, получили поток %v", events)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("ошибка = %q, ожидали упоминание %q", err, c.want)
			}
		})
	}
}

// Ошибка поиска endpoint останавливает пайплайн с понятным сообщением.
func TestRunLocatorError(t *testing.T) {
	fleet := &tunnelFleet{}
	loc := &fakeLocator{err: errors.New("сеть недоступна")}
	events, cancel := startRun(t, testConfig(fleet, loc, &fakeChain{}, &fakeProber{}))
	defer cancel()

	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || !strings.Contains(last.Err, "сеть недоступна") {
		t.Fatalf("остановка: %+v", last)
	}
}

// Пустой список endpoint'ов — остановка с подсказкой.
func TestRunNoEndpoints(t *testing.T) {
	fleet := &tunnelFleet{}
	loc := &fakeLocator{}
	events, cancel := startRun(t, testConfig(fleet, loc, &fakeChain{}, &fakeProber{}))
	defer cancel()

	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || !strings.Contains(last.Err, "endpoint не найдено") {
		t.Fatalf("остановка: %+v", last)
	}
}

// События не несут секретов: ключи и токены в них не попадают.
func TestEventsCarryNoSecrets(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	events, cancel := startRun(t, testConfig(fleet, loc, ch, pr))
	nextUntil(t, events, EventReady, "готовность")
	cancel()
	evs := drain(t, events)

	for _, ev := range evs {
		if strings.Contains(ev.String(), "private") || strings.Contains(ev.String(), "token") {
			t.Errorf("секрет в событии: %+v", ev)
		}
	}
}

// Сборка событий не блокирует пайплайн: даже если читатель молчит, Run продолжает
// работать и корректно останавливается.
func TestRunDoesNotBlockWithoutReader(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.MonitorInterval = time.Millisecond
	events, cancel := startRun(t, cfg)

	// Не читаем поток, даём мониторингу натикать и убеждаемся, что цепочка жива.
	time.Sleep(100 * time.Millisecond)
	cancel()

	done := make(chan []Event, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		done <- drain(t, events)
	}()
	wg.Wait()

	evs := <-done
	if last := evs[len(evs)-1]; last.Kind != EventStopped {
		t.Fatalf("остановка: %+v", last)
	}
	if calls, _, open := ch.counts(); calls == 0 || open != 0 {
		t.Errorf("цепочка: вызовов %d, живо сессий %d", calls, open)
	}
}
