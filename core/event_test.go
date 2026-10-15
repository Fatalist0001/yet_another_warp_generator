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
		{"выбор endpoint", Event{Kind: EventEndpointSelected, Endpoint: "162.159.192.1:2408", Colo: "HEL", Region: "RU", RTTMS: 84},
			[]string{"162.159.192.1:2408", "HEL", "RU", "84ms"}},
		{"туннель поднят", Event{Kind: EventOuterUp, Endpoint: "162.159.192.1:2408"}, []string{"162.159.192.1:2408"}},
		{"трафик проверен", Event{Kind: EventOuterChecked, Colo: "FRA"}, []string{"warp=on", "FRA"}},
		{"форвардер", Event{Kind: EventForwarderUp, Forwarder: "127.0.0.1:51000"}, []string{"127.0.0.1:51000"}},
		{"страна", Event{Kind: EventPsiphonStarting, Country: "NL"}, []string{"NL", "warp-plus"}},
		{"готовность", Event{Kind: EventReady, SOCKS: "127.0.0.1:8086", Country: "NL", IP: "203.0.113.9", Org: "AS123 Test", Seconds: 42},
			[]string{"socks5://127.0.0.1:8086", "NL", "203.0.113.9", "AS123 Test", "42s"}},
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
	for _, key := range []string{"endpoint", "colo", "region", "reason", "error", "fails"} {
		if _, ok := got[key]; ok {
			t.Errorf("пустое поле %q не должно попадать в JSON: %s", key, data)
		}
	}
	if got["event"] != "ready" {
		t.Errorf("event = %v", got["event"])
	}
}

// Имена событий из задания — часть контракта для CLI и внешних потребителей.
func TestEventNames(t *testing.T) {
	want := map[Kind]string{
		EventEndpointSelected: "endpoint_selected",
		EventOuterUp:          "outer_up",
		EventOuterChecked:     "outer_checked",
		EventForwarderUp:      "forwarder_up",
		EventPsiphonStarting:  "psiphon_starting",
		EventReady:            "ready",
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
