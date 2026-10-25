package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Замер скорости должен сортировать кандидатов именно по скорости, а RTT
// остаётся только tie-breaker.
func TestSelectFastestOrdersBySpeed(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	cfg := testConfig(fleet, &fakeLocator{}, &fakeChain{}, &fakeProber{})
	cfg.Direct = true
	cfg.Countries = nil
	speeds := map[string]float64{
		"162.159.192.1:2408": 10,
		"162.159.192.2:2408": 50,
		"162.159.192.3:2408": 20,
	}
	cfg.SpeedTest = func(_ context.Context, _, endpoint string) (SpeedResult, error) {
		return SpeedResult{Mbps: speeds[endpoint], Bytes: 1024}, nil
	}
	r := &runner{cfg: cfg.withDefaults(), clock: cfg.Clock, events: make(chan Event, 32), proxy: &SocksProxy{}}

	ranked, measured := r.selectFastest(context.Background(), []Endpoint{
		{Addr: "162.159.192.1:2408", RTT: 10 * time.Millisecond},
		{Addr: "162.159.192.2:2408", RTT: 50 * time.Millisecond},
		{Addr: "162.159.192.3:2408", RTT: 30 * time.Millisecond},
	})
	if !measured {
		t.Fatal("ожидали хотя бы один успешный замер")
	}
	want := []string{
		"162.159.192.2:2408",
		"162.159.192.3:2408",
		"162.159.192.1:2408",
	}
	for i := range want {
		if ranked[i].Addr != want[i] {
			t.Errorf("позиция %d: %s, ожидали %s", i, ranked[i].Addr, want[i])
		}
	}
	if ranked[0].SpeedMbps != 50 {
		t.Errorf("скорость первого = %v, ожидали 50", ranked[0].SpeedMbps)
	}
}

// В режиме с Psiphon замер идёт через SOCKS5 цепочки, а не через внешний WARP.
func TestMeasureEndpointSpeedUsesChainSocks(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	ch := &fakeChain{socks: "127.0.0.1:19999"}
	cfg := testConfig(fleet, &fakeLocator{}, ch, &fakeProber{})
	var gotSocks string
	cfg.SpeedTest = func(_ context.Context, socks, _ string) (SpeedResult, error) {
		gotSocks = socks
		return SpeedResult{Mbps: 12.5, Bytes: 2048}, nil
	}
	r := &runner{cfg: cfg.withDefaults(), events: make(chan Event, 16)}

	res, err := r.measureEndpointSpeed(context.Background(), Endpoint{Addr: "162.159.192.1:2408"})
	if err != nil {
		t.Fatalf("measureEndpointSpeed: %v", err)
	}
	if res.Mbps != 12.5 {
		t.Errorf("скорость = %v, ожидали 12.5", res.Mbps)
	}
	if gotSocks != "127.0.0.1:19999" {
		t.Errorf("замер шёл через %q, ожидали SOCKS5 цепочки", gotSocks)
	}
}

// Готовый кэш должен исключать и скан, и повторный замер скорости.
func TestPipelineUsesCachedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.json")
	if err := SaveProfile(path, ProfileWarpOnly, nil, []Endpoint{{
		Addr:      "162.159.192.1:2408",
		Colo:      "HEL",
		Loc:       "RU",
		RTT:       42 * time.Millisecond,
		SpeedMbps: 99,
	}}); err != nil {
		t.Fatal(err)
	}

	fleet := &tunnelFleet{echo: udpEcho(t)}
	loc := &fakeLocator{endpoints: []Endpoint{{Addr: "162.159.192.2:2408"}}}
	cfg := testConfig(fleet, loc, &fakeChain{}, &fakeProber{})
	cfg.Direct = true
	cfg.Countries = nil
	cfg.Bind = "127.0.0.1:0"
	cfg.ProfilePath = path
	speedCalls := 0
	cfg.SpeedTest = func(context.Context, string, string) (SpeedResult, error) {
		speedCalls++
		return SpeedResult{Mbps: 1}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	selected := expectEvent(t, events, EventEndpointSelected, "кэш")
	if selected.Endpoint != "162.159.192.1:2408" {
		t.Errorf("endpoint = %s, ожидали сохранённый", selected.Endpoint)
	}
	if selected.Mbps != 99 {
		t.Errorf("скорость из кэша = %v, ожидали 99", selected.Mbps)
	}
	nextUntil(t, events, EventReady, "кэш")
	cancel()
	drain(t, events)

	if loc.calls != 0 {
		t.Errorf("Locate вызван %d раз, хотя есть кэш", loc.calls)
	}
	if speedCalls != 0 {
		t.Errorf("замер скорости вызван %d раз, хотя есть кэш", speedCalls)
	}
}

// Если сохранённый endpoint умер, кэш сбрасывается, скан и замер повторяются,
// а новый профиль сохраняется для следующих запусков.
func TestPipelineRefreshesStaleProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.json")
	if err := SaveProfile(path, ProfileWarpOnly, nil, []Endpoint{{
		Addr: "162.159.192.9:2408",
	}}); err != nil {
		t.Fatal(err)
	}

	fleet := &tunnelFleet{
		echo: udpEcho(t),
		HandshakeFn: func(call int, _ string) bool {
			return call != 1
		},
	}
	loc := &fakeLocator{endpoints: []Endpoint{{Addr: "162.159.192.1:2408"}}}
	cfg := testConfig(fleet, loc, &fakeChain{}, &fakeProber{})
	cfg.Direct = true
	cfg.Countries = nil
	cfg.Bind = "127.0.0.1:0"
	cfg.ProfilePath = path
	var tested string
	cfg.SpeedTest = func(_ context.Context, _, endpoint string) (SpeedResult, error) {
		tested = endpoint
		return SpeedResult{Mbps: 25}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	nextUntil(t, events, EventReady, "обновление кэша")
	cancel()
	drain(t, events)

	if loc.calls != 1 {
		t.Errorf("Locate вызван %d раз, ожидали 1 после сброса кэша", loc.calls)
	}
	if tested != "162.159.192.1:2408" {
		t.Errorf("замер шёл для %q, ожидали новый endpoint", tested)
	}
	got, ok, err := LoadProfile(path, ProfileWarpOnly, nil)
	if err != nil || !ok {
		t.Fatalf("новый профиль: ok=%v err=%v", ok, err)
	}
	if got[0].Addr != "162.159.192.1:2408" || got[0].SpeedMbps != 25 {
		t.Errorf("профиль после обновления = %+v", got[0])
	}
}
