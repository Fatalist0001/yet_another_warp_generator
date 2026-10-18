package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
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

// tunConfig — конфигурация happy path с системным туннелем.
func tunConfig(t *testing.T, fleet *tunnelFleet, loc *fakeLocator, ch *fakeChain, pr *fakeProber, tun *fakeTUN) Config {
	t.Helper()
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.TUN = tun.factory()
	return cfg
}

// stepsAfter возвращает вызовы системного туннеля после шага с указанным номером.
func stepCalls(steps []tunStep, call string) []tunStep {
	var out []tunStep
	for _, s := range steps {
		if s.call == call {
			out = append(out, s)
		}
	}
	return out
}

// В режиме --tun маршруты применяются после ready, а снимаются до остановки цепочки.
func TestRunTunStartsAfterReady(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	tun := &fakeTUN{}
	events, cancel := startRun(t, tunConfig(t, fleet, loc, ch, pr, tun))

	ready := nextUntil(t, events, EventReady, "готовность")
	if ready.SOCKS != "127.0.0.1:8086" {
		t.Fatalf("ready = %+v", ready)
	}
	// Маршруты применяются только после ready: событие tun_up идёт за ним.
	up := expectEvent(t, events, EventTunUp, "после готовности")
	if up.TUN != "warpgen0" || up.MTU != 1400 {
		t.Errorf("tun_up = %+v", up)
	}
	if applied := expectEvent(t, events, EventRoutesApplied, "после TUN"); applied.Routes != 9 {
		t.Errorf("routes_applied = %+v", applied)
	}
	if sel := expectEvent(t, events, EventDNSSelected, "после маршрутов"); sel.DNS != "1.1.1.1:53" || sel.RTTMS != 20 {
		t.Errorf("dns_selected = %+v", sel)
	}

	cancel()
	evs := drain(t, events)
	if len(evs) != 2 || evs[0].Kind != EventRoutesReverted || evs[1].Kind != EventStopped {
		t.Fatalf("остановка: %+v", evs)
	}
	if evs[1].Err != "" {
		t.Errorf("остановка с ошибкой: %q", evs[1].Err)
	}

	created, starts, stops := tun.counts()
	if created != 1 || starts != 1 || stops != 1 {
		t.Errorf("туннель: создан %d, запусков %d, остановок %d", created, starts, stops)
	}
	starts_ := stepCalls(tun.calls(), "start")
	if len(starts_) != 1 || starts_[0].socks != "127.0.0.1:8086" || starts_[0].endpoint != "162.159.192.1:2408" {
		t.Errorf("старт туннеля: %+v", starts_)
	}
	if calls, closed, open := ch.counts(); calls != 1 || closed != 1 || open != 0 {
		t.Errorf("цепочка: вызовов %d, закрыто %d, живо %d", calls, closed, open)
	}
	if createdTunnels, closedTunnels := fleet.counts(); createdTunnels != 1 || closedTunnels != 1 {
		t.Errorf("туннели: создано %d, закрыто %d", createdTunnels, closedTunnels)
	}
}

// При смене страны маршруты не снимаются: мост просто переезжает на новый SOCKS.
func TestRunTunKeepsRoutesOnFailover(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	// RU виден три проверки подряд (RUFailures по умолчанию): только после этого
	// меняем страну, одиночные ответы RU не считаются сбоем.
	pr := &fakeProber{steps: []probeStep{
		{res: ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}},
		{res: ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}},
		{res: ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}},
		{res: healthyProbe()},
	}}
	tun := &fakeTUN{}
	cfg := tunConfig(t, fleet, loc, ch, pr, tun)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	nextUntil(t, events, EventReady, "первая готовность")
	nextUntil(t, events, EventDNSSelected, "DNS первого запуска")
	nextUntil(t, events, EventFailover, "выход RU")
	nextUntil(t, events, EventReady, "готовность после смены страны")
	nextUntil(t, events, EventDNSSelected, "DNS после смены страны")

	cancel()
	evs := drain(t, events)
	if len(evs) != 2 || evs[0].Kind != EventRoutesReverted {
		t.Fatalf("остановка: %+v", evs)
	}

	created, starts, stops := tun.counts()
	if created != 1 || starts != 1 || stops != 1 {
		t.Errorf("туннель: создан %d, запусков %d, остановок %d — при failover его не пересоздают", created, starts, stops)
	}
	socks := stepCalls(tun.calls(), "socks")
	if len(socks) != 1 || socks[0].socks != "127.0.0.1:8086" {
		t.Errorf("смена SOCKS5: %+v", socks)
	}
	// Маршруты не пересоздавали: исключение endpoint осталось прежним.
	if got := stepCalls(tun.calls(), "endpoint"); len(got) != 0 {
		t.Errorf("при смене страны endpoint не менялся: %+v", got)
	}
}

// Смена внешнего endpoint обновляет исключение /32 и переводит мост на новый SOCKS.
func TestRunTunUpdatesExclusionOnEndpointChange(t *testing.T) {
	// TraceFailures по умолчанию = 3: мёртвым туннель считается только после
	// трёх неудачных проверок подряд (вызовы 2–4), дальше снова работает.
	fleet := &tunnelFleet{TraceFn: func(call int) (tunnel.TraceResult, error) {
		if call == 1 || call >= 5 {
			return traceOK(), nil
		}
		return tunnel.TraceResult{}, errors.New("таймаут")
	}}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	tun := &fakeTUN{}
	events, cancel := startRun(t, tunConfig(t, fleet, loc, ch, &fakeProber{}, tun))
	defer cancel()

	nextUntil(t, events, EventDNSSelected, "первый запуск")
	nextUntil(t, events, EventReady, "готовность после пересоздания")
	nextUntil(t, events, EventDNSSelected, "DNS после пересоздания")

	cancel()
	drain(t, events)

	calls := tun.calls()
	// Порядок: start → (смена endpoint) → socks. Исключение обновляется до
	// создания нового туннеля, иначе его сокет зациклится в TUN.
	var order []string
	for _, s := range calls {
		order = append(order, s.call)
	}
	want := []string{"start", "endpoint", "socks", "stop"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("порядок вызовов: %v, ожидали %v", order, want)
	}
	if eps := stepCalls(calls, "endpoint"); len(eps) != 1 || eps[0].endpoint != "162.159.192.1:2408" {
		t.Errorf("исключение endpoint: %+v", eps)
	}
}

// Ошибка запуска системного туннеля останавливает пайплайн и освобождает цепочку.
func TestRunTunStartFailure(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	tun := &fakeTUN{StartErr: errors.New("нет прав на TUN")}
	events, cancel := startRun(t, tunConfig(t, fleet, loc, ch, pr, tun))
	defer cancel()

	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || !strings.Contains(last.Err, "системный туннель") {
		t.Fatalf("остановка: %+v", last)
	}
	if strings.Contains(last.Err, "нет прав") == false {
		t.Errorf("текст ошибки = %q", last.Err)
	}
	for _, ev := range evs {
		if ev.Kind == EventRoutesApplied {
			t.Errorf("маршруты не должны применяться: %+v", ev)
		}
	}
	if calls, closed, open := ch.counts(); calls != 1 || closed != 1 || open != 0 {
		t.Errorf("цепочка: вызовов %d, закрыто %d, живо %d", calls, closed, open)
	}
	if _, _, stops := tun.counts(); stops != 1 {
		t.Errorf("остановок туннеля %d, ожидали 1", stops)
	}
}

// Ошибка перевода моста на новый SOCKS останавливает пайплайн и снимает маршруты.
func TestRunTunSocksUpdateFailure(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	pr := &fakeProber{steps: []probeStep{
		{res: ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}},
	}}
	tun := &fakeTUN{SocksErr: errors.New("мост не отвечает")}
	cfg := tunConfig(t, fleet, loc, ch, pr, tun)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	nextUntil(t, events, EventFailover, "выход RU")
	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || !strings.Contains(last.Err, "смена SOCKS5") {
		t.Fatalf("остановка: %+v", last)
	}
	var reverted bool
	for _, ev := range evs {
		if ev.Kind == EventRoutesReverted {
			reverted = true
		}
	}
	if !reverted {
		t.Errorf("маршруты должны быть сняты: %+v", evs)
	}
}

// Сбой выбора DNS не останавливает пайплайн: маршруты уже применены.
func TestRunTunDNSFailureIsNotFatal(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	tun := &fakeTUN{Status: vpn.Status{DNSErr: "ни один DNS не ответил через цепочку"}}
	events, cancel := startRun(t, tunConfig(t, fleet, loc, ch, pr, tun))
	defer cancel()

	nextUntil(t, events, EventTunUp, "TUN поднят")
	failed := nextUntil(t, events, EventDNSFailed, "отказ DNS")
	if !strings.Contains(failed.Reason, "ни один DNS") {
		t.Errorf("dns_failed = %+v", failed)
	}

	cancel()
	evs := drain(t, events)
	last := evs[len(evs)-1]
	if last.Kind != EventStopped || last.Err != "" {
		t.Errorf("остановка: %+v", last)
	}
}

// Без флага --tun системный туннель не создаётся, а события о нём не появляются.
func TestRunWithoutTunHasNoTunEvents(t *testing.T) {
	fleet, loc, ch, pr := happyPath(t)
	events, cancel := startRun(t, testConfig(fleet, loc, ch, pr))
	nextUntil(t, events, EventReady, "готовность")
	cancel()

	for _, ev := range drain(t, events) {
		switch ev.Kind {
		case EventTunUp, EventRoutesApplied, EventRoutesReverted, EventDNSSelected, EventDNSFailed:
			t.Errorf("событие системного туннеля без --tun: %+v", ev)
		}
	}
}

// В режиме --tun после остановки не остаётся лишних горутин.
func TestRunTunLeavesNoGoroutines(t *testing.T) {
	base := runtime.NumGoroutine()

	fleet, loc, ch, pr := happyPath(t)
	tun := &fakeTUN{}
	events, cancel := startRun(t, tunConfig(t, fleet, loc, ch, pr, tun))
	nextUntil(t, events, EventDNSSelected, "готовность с TUN")
	cancel()
	drain(t, events)
	closedChannel(t, events)

	if _, _, stops := tun.counts(); stops != 1 {
		t.Errorf("остановок туннеля %d, ожидали 1", stops)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.Gosched()
		if n := runtime.NumGoroutine(); n <= base+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("после остановки осталось %d горутин (было %d)", runtime.NumGoroutine(), base)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// RU на выходе через SOCKS5 меняет страну только после RUFailures проверок
// подряд: одиночные ответы RU — это просадка, а не сломанное звено.
func TestRunMonitorFailsOverOnRU(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	ru := ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}
	pr := &fakeProber{steps: []probeStep{{res: ru}, {res: ru}, {res: ru}, {res: healthyProbe()}}}
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)
	defer cancel()

	ready := nextUntil(t, events, EventReady, "первая готовность")
	if ready.Country != "NL" {
		t.Errorf("первая страна = %q, ожидали NL", ready.Country)
	}
	for want := 1; want <= 3; want++ {
		ev := nextUntil(t, events, EventDegraded, "выход RU")
		if ev.Fails != want || !strings.Contains(ev.Reason, "RU") {
			t.Errorf("просадка %d = %+v", want, ev)
		}
	}
	fail := nextUntil(t, events, EventFailover, "смена страны после RU")
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

// Одиночный (или парный) замер RU не меняет звено: мониторинг инерционный.
func TestRunMonitorSingleRUDoesNotFailover(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{}
	ru := ProbeResult{Country: "RU", IP: "95.1.2.3", Services: 2, RU: true}
	pr := &fakeProber{steps: []probeStep{{res: ru}, {res: healthyProbe()}}}
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)

	nextUntil(t, events, EventReady, "готовность")
	// Ждём, пока мониторинг сделает заметно больше проверок, чем порог.
	deadline := time.Now().Add(2 * time.Second)
	for pr.count() < DefaultRUFailures+3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	for _, ev := range drain(t, events) {
		if ev.Kind == EventFailover || ev.Kind == EventReady {
			t.Errorf("одиночный RU не должен менять звено: %+v", ev)
		}
	}
	if calls, _, _ := ch.counts(); calls != 1 {
		t.Errorf("цепочка запускалась %d раз, ожидали один", calls)
	}
}

// Геосервисы молчат, но связь через SOCKS5 жива (Prober вернул пустой
// результат без ошибки) — звено не меняем, только сообщаем «выход не проверен».
func TestRunMonitorGeoSilentDoesNotFailover(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{}
	pr := &fakeProber{steps: []probeStep{{res: ProbeResult{}}}} // Services=0, без ошибки
	cfg := testConfig(fleet, loc, ch, pr)
	cfg.Countries = []string{"NL", "SE"}
	events, cancel := startRun(t, cfg)

	nextUntil(t, events, EventReady, "готовность")
	saw := false
	deadline := time.Now().Add(2 * time.Second)
	for pr.count() < DefaultMonitorFailures+2 && time.Now().Before(deadline) {
		select {
		case ev := <-events:
			if ev.Kind == EventDegraded {
				saw = true
				if !strings.Contains(ev.Reason, "не проверен") {
					t.Errorf("причина degraded = %q", ev.Reason)
				}
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	if !saw {
		t.Error("ожидали событие degraded о непроверенном выходе")
	}
	for _, ev := range drain(t, events) {
		if ev.Kind == EventFailover || ev.Kind == EventReady {
			t.Errorf("молчание геосервисов не должно менять звено: %+v", ev)
		}
	}
	if calls, _, _ := ch.counts(); calls != 1 {
		t.Errorf("цепочка запускалась %d раз, ожидали один", calls)
	}
}

// MonitorFailures сбоев подряд — смена страны; предыдущие дают degraded.
func TestRunMonitorDegradedThenFailover(t *testing.T) {
	fleet, loc, _, _ := happyPath(t)
	ch := &fakeChain{steps: []chainStep{{}, {}}}
	pr := &fakeProber{steps: []probeStep{
		{err: errors.New("нет ответа")},
		{err: errors.New("нет ответа")},
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
	for want := 1; want <= DefaultMonitorFailures; want++ {
		ev := nextUntil(t, events, EventDegraded, "просадка")
		if ev.Fails != want {
			t.Errorf("сбой номер = %d, ожидали %d", ev.Fails, want)
		}
	}
	fail := nextUntil(t, events, EventFailover, "смена страны по сбоям")
	if !strings.Contains(fail.Reason, fmt.Sprintf("%d сбоя", DefaultMonitorFailures)) {
		t.Errorf("причина = %q, ожидали «%d сбоя»", fail.Reason, DefaultMonitorFailures)
	}
	if next := nextUntil(t, events, EventReady, "готовность после сбоев"); next.Country != "SE" {
		t.Errorf("страна = %q, ожидали SE", next.Country)
	}

	cancel()
	drain(t, events)
}

// Внешний туннель перестал отвечать — пересоздаём его и форвардер на следующем endpoint.
// Туннель считается мёртвым только после TraceFailures неудачных проверок подряд.
func TestRunRecreatesOuterTunnel(t *testing.T) {
	fleet := &tunnelFleet{TraceFn: func(call int) (tunnel.TraceResult, error) {
		if call == 1 || call >= 1+DefaultTraceFailures+1 {
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

// directConfig — конфигурация режима без Psiphon: SOCKS5 поверх туннеля, без
// цепочки и списка стран.
func directConfig(t *testing.T, fleet *tunnelFleet, loc *fakeLocator, pr *fakeProber) Config {
	t.Helper()
	cfg := testConfig(fleet, loc, nil, pr)
	cfg.Direct = true
	cfg.Countries = nil
	cfg.Bind = "127.0.0.1:0"
	return cfg
}

// Режим без Psiphon: наружу отдаётся SOCKS5 поверх самого туннеля, трафик ходит,
// а российский выход не считается сбоем.
func TestRunWarpOnlyServesSocks(t *testing.T) {
	echo := tcpEcho(t)
	fleet := &tunnelFleet{tcpEcho: echo}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	pr := &fakeProber{steps: []probeStep{{
		res: ProbeResult{Country: "RU", IP: "95.1.2.3", Org: "AS1 RU", Services: 2, RU: true},
	}}}
	events, cancel := startRun(t, directConfig(t, fleet, loc, pr))

	expectEvent(t, events, EventEndpointSelected, "старт")
	expectEvent(t, events, EventOuterUp, "туннель поднят")
	expectEvent(t, events, EventOuterChecked, "проверка туннеля")
	socks := expectEvent(t, events, EventSocksUp, "SOCKS5 без Psiphon")
	if !strings.HasPrefix(socks.SOCKS, "127.0.0.1:") {
		t.Errorf("адрес SOCKS5 = %q, ожидали loopback", socks.SOCKS)
	}
	ready := expectEvent(t, events, EventReady, "готовность")
	if ready.SOCKS != socks.SOCKS || ready.Country != "RU" || ready.IP != "203.0.113.1" {
		t.Errorf("ready = %+v", ready)
	}

	// Трафик через SOCKS5 действительно уходит в туннель (локальное TCP-эхо).
	conn := socksConnect(t, ready.SOCKS, echo)
	defer conn.Close()
	if _, err := conn.Write([]byte("warp")); err != nil {
		t.Fatalf("запись через SOCKS5: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("чтение через SOCKS5: %v", err)
	}
	if string(buf[:n]) != "warp" {
		t.Errorf("вернулось %q", buf[:n])
	}

	// Даём мониторингу сработать: выход RU в этом режиме не меняет звено.
	deadline := time.Now().Add(2 * time.Second)
	for pr.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pr.count() == 0 {
		t.Error("мониторинг ни разу не проверил выход через SOCKS5")
	}

	cancel()
	evs := drain(t, events)
	for _, ev := range evs {
		switch ev.Kind {
		case EventForwarderUp, EventPsiphonStarting:
			t.Errorf("в режиме без Psiphon лишнее событие: %+v", ev)
		case EventFailover:
			t.Errorf("выход RU не должен менять звено: %+v", ev)
		}
	}
	if last := evs[len(evs)-1]; last.Kind != EventStopped || last.Err != "" {
		t.Errorf("остановка: %+v", last)
	}
}

// Режим без Psiphon с --tun: мост идёт на локальный SOCKS5, маршруты применяются
// после ready, остановка снимает всё.
func TestRunWarpOnlyWithTUN(t *testing.T) {
	fleet := &tunnelFleet{tcpEcho: tcpEcho(t)}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	tun := &fakeTUN{}
	cfg := directConfig(t, fleet, loc, &fakeProber{})
	cfg.TUN = tun.factory()
	events, cancel := startRun(t, cfg)

	socks := nextUntil(t, events, EventSocksUp, "SOCKS5")
	ready := nextUntil(t, events, EventReady, "готовность")
	tunUp := nextUntil(t, events, EventTunUp, "TUN")
	if tunUp.TUN != "warpgen0" {
		t.Errorf("tun_up = %+v", tunUp)
	}
	applied := nextUntil(t, events, EventRoutesApplied, "маршруты")
	if applied.Routes != 9 {
		t.Errorf("routes_applied = %+v", applied)
	}
	nextUntil(t, events, EventDNSSelected, "DNS")

	cancel()
	evs := drain(t, events)
	if len(evs) != 2 || evs[0].Kind != EventRoutesReverted || evs[1].Kind != EventStopped {
		t.Fatalf("остановка: %+v", evs)
	}

	starts := stepCalls(tun.calls(), "start")
	if len(starts) != 1 {
		t.Fatalf("запуск TUN: %+v", starts)
	}
	if starts[0].socks != socks.SOCKS || starts[0].socks != ready.SOCKS {
		t.Errorf("TUN поднят на %q, а SOCKS5 — %q", starts[0].socks, socks.SOCKS)
	}
	if starts[0].endpoint != "162.159.192.1:2408" {
		t.Errorf("исключение endpoint = %q", starts[0].endpoint)
	}
}

// Сбои проверки выхода в режиме без Psiphon ведут к пересозданию туннеля на
// следующем endpoint'е (менять страну нельзя — её нет).
func TestRunWarpOnlyRecreatesTunnelOnProbeFailures(t *testing.T) {
	fleet := &tunnelFleet{tcpEcho: tcpEcho(t)}
	loc := &fakeLocator{endpoints: []Endpoint{endpoint162("162.159.192.1:2408")}}
	pr := &fakeProber{steps: []probeStep{{err: errors.New("нет ответа")}}}
	events, cancel := startRun(t, directConfig(t, fleet, loc, pr))
	defer cancel()

	nextUntil(t, events, EventReady, "готовность")
	for want := 1; want <= DefaultMonitorFailures; want++ {
		ev := nextUntil(t, events, EventDegraded, "просадка")
		if ev.Fails != want {
			t.Errorf("сбой номер = %d, ожидали %d", ev.Fails, want)
		}
	}
	fail := nextUntil(t, events, EventFailover, "пересоздание туннеля")
	if !strings.Contains(fail.Reason, "нет выхода через SOCKS5") {
		t.Errorf("причина = %q", fail.Reason)
	}
	next := nextUntil(t, events, EventEndpointSelected, "новый endpoint")
	if next.Endpoint != "162.159.192.1:2408" {
		t.Errorf("endpoint = %q", next.Endpoint)
	}
	nextUntil(t, events, EventSocksUp, "SOCKS5 после пересоздания")
	if ready, _ := fleet.counts(); ready != 2 {
		t.Errorf("туннелей создано %d, ожидали 2", ready)
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
