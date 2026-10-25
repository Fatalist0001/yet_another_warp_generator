package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventString(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want []string
	}{
		{"выбор endpoint", Event{Kind: EventEndpointSelected, Endpoint: "162.159.192.1:2408", Colo: "HEL", Region: "RU", RTTMS: 84, Mbps: 42.5},
			[]string{"162.159.192.1:2408", "HEL", "RU", "84ms", "42.5"}},
		{"туннель поднят", Event{Kind: EventOuterUp, Endpoint: "162.159.192.1:2408"}, []string{"162.159.192.1:2408"}},
		{"трафик проверен", Event{Kind: EventOuterChecked, Colo: "FRA"}, []string{"warp=on", "FRA"}},
		{"замер скорости", Event{Kind: EventSpeedTesting, Endpoint: "162.159.192.1:2408"}, []string{"162.159.192.1:2408", "скорость"}},
		{"скорость измерена", Event{Kind: EventSpeedChecked, Endpoint: "162.159.192.1:2408", Mbps: 12.34}, []string{"162.159.192.1:2408", "12.3"}},
		{"форвардер", Event{Kind: EventForwarderUp, Forwarder: "127.0.0.1:51000"}, []string{"127.0.0.1:51000"}},
		{"страна", Event{Kind: EventPsiphonStarting, Country: "NL"}, []string{"NL", "warp-plus"}},
		{"готовность", Event{Kind: EventReady, SOCKS: "127.0.0.1:8086", Country: "NL", IP: "203.0.113.9", Org: "AS123 Test", Seconds: 42},
			[]string{"socks5://127.0.0.1:8086", "NL", "203.0.113.9", "AS123 Test", "42s"}},
		{"системный туннель", Event{Kind: EventTunUp, TUN: "warpgen0", MTU: 1400}, []string{"warpgen0", "1400"}},
		{"маршруты применены", Event{Kind: EventRoutesApplied, TUN: "warpgen0", Routes: 9}, []string{"warpgen0", "9"}},
		{"маршруты сняты", Event{Kind: EventRoutesReverted}, []string{"маршруты", "DNS"}},
		{"DNS через цепочку", Event{Kind: EventDNSSelected, DNS: "1.1.1.1:53", RTTMS: 20}, []string{"1.1.1.1:53", "20ms"}},
		{"DNS не настроен", Event{Kind: EventDNSFailed, Reason: "нет resolvectl"}, []string{"нет resolvectl"}},
		{"просадка", Event{Kind: EventDegraded, Fails: 2, Reason: "нет ответа"}, []string{"2", "нет ответа"}},
		{"смена звена", Event{Kind: EventFailover, Reason: "нет хендшейка"}, []string{"нет хендшейка"}},
		{"остановка", Event{Kind: EventStopped}, []string{"остановлено"}},
		{"остановка с ошибкой", Event{Kind: EventStopped, Err: "нет endpoint"}, []string{"нет endpoint"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.ev.String()
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("String() = %q, нет %q", got, want)
				}
			}
		})
	}
}

// Потребитель по JSON видит только нужные поля события и никаких секретов.
func TestEventJSONShape(t *testing.T) {
	ev := Event{
		Kind: EventReady, Time: time.Unix(0, 0).UTC(),
		SOCKS: "127.0.0.1:8086", Country: "NL", IP: "203.0.113.9", Org: "AS123 Test", Seconds: 42,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"event", "time", "socks", "country", "ip", "org", "seconds"} {
		if _, ok := got[key]; !ok {
			t.Errorf("в JSON нет ключа %q: %s", key, data)
		}
	}
	for _, key := range []string{"endpoint", "colo", "region", "rtt_ms", "mbps", "bytes", "reason", "error", "fails", "tun", "mtu", "routes", "dns"} {
		if _, ok := got[key]; ok {
			t.Errorf("пустое поле %q не должно попадать в JSON: %s", key, data)
		}
	}
	if got["event"] != "ready" {
		t.Errorf("event = %v", got["event"])
	}
}

// События системного туннеля не несут лишних ключей и не содержат секретов.
func TestTunEventJSONShape(t *testing.T) {
	cases := []struct {
		ev   Event
		have []string
		miss []string
	}{
		{Event{Kind: EventTunUp, TUN: "warpgen0", MTU: 1400}, []string{"tun", "mtu"}, []string{"routes", "dns"}},
		{Event{Kind: EventRoutesApplied, TUN: "warpgen0", Routes: 9}, []string{"tun", "routes"}, []string{"mtu", "dns"}},
		{Event{Kind: EventRoutesReverted}, nil, []string{"tun", "mtu", "routes", "dns"}},
		{Event{Kind: EventDNSSelected, DNS: "1.1.1.1:53", RTTMS: 20}, []string{"dns", "rtt_ms"}, []string{"tun", "routes"}},
		{Event{Kind: EventDNSFailed, Reason: "нет resolvectl"}, []string{"reason"}, []string{"dns", "tun"}},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.ev)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		for _, key := range c.have {
			if _, ok := got[key]; !ok {
				t.Errorf("%s: в JSON нет ключа %q: %s", c.ev.Kind, key, data)
			}
		}
		for _, key := range c.miss {
			if _, ok := got[key]; ok {
				t.Errorf("%s: лишний ключ %q: %s", c.ev.Kind, key, data)
			}
		}
	}
}

// Имена событий из задания — часть контракта для CLI и внешних потребителей.
func TestEventNames(t *testing.T) {
	want := map[Kind]string{
		EventEndpointSelected: "endpoint_selected",
		EventOuterUp:          "outer_up",
		EventOuterChecked:     "outer_checked",
		EventSpeedTesting:     "speed_testing",
		EventSpeedChecked:     "speed_checked",
		EventForwarderUp:      "forwarder_up",
		EventPsiphonStarting:  "psiphon_starting",
		EventReady:            "ready",
		EventTunUp:            "tun_up",
		EventRoutesApplied:    "routes_applied",
		EventRoutesReverted:   "routes_reverted",
		EventDNSSelected:      "dns_selected",
		EventDNSFailed:        "dns_failed",
		EventDegraded:         "degraded",
		EventFailover:         "failover",
		EventStopped:          "stopped",
	}
	for kind, name := range want {
		if string(kind) != name {
			t.Errorf("%q, ожидали %q", kind, name)
		}
	}
}
