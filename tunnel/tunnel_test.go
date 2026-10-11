package tunnel

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseTrace(t *testing.T) {
	body := "fl=123\nip=1.2.3.4\nloc=FI\ncolo=HEL\nwarp=on\n"
	tr := ParseTrace(body)
	if tr.Loc != "FI" {
		t.Errorf("Loc = %q, want FI", tr.Loc)
	}
	if tr.Colo != "HEL" {
		t.Errorf("Colo = %q, want HEL", tr.Colo)
	}
	if tr.IP != "1.2.3.4" {
		t.Errorf("IP = %q, want 1.2.3.4", tr.IP)
	}
	if tr.Raw != body {
		t.Error("Raw не совпадает с телом ответа")
	}
}

func TestParseTraceEmpty(t *testing.T) {
	tr := ParseTrace("")
	if tr.Loc != "" || tr.Colo != "" || tr.IP != "" {
		t.Errorf("пустой trace должен дать пустые поля: %+v", tr)
	}
}

func TestHandshakeDone(t *testing.T) {
	if handshakeDone("private_key=abc\nlast_handshake_time_sec=0\n") {
		t.Error("нулевой хендшейк не должен считаться завершённым")
	}
	if !handshakeDone("last_handshake_time_sec=1728000000\n") {
		t.Error("ненулевой хендшейк должен считаться завершённым")
	}
	if handshakeDone("private_key=abc\n") {
		t.Error("без поля хендшейка результат должен быть false")
	}
}

func TestBase64ToHex(t *testing.T) {
	raw := make([]byte, 32)
	raw[0] = 0xab
	got, err := base64ToHex(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("base64ToHex: %v", err)
	}
	want := "ab" + "00000000000000000000000000000000000000000000000000000000000000"
	if len(got) != 64 || got[:2] != "ab" {
		t.Errorf("base64ToHex = %s, want 64 hex chars starting ab (want=%s)", got, want)
	}
}

func TestBase64ToHexInvalid(t *testing.T) {
	if _, err := base64ToHex("not base64!!"); err == nil {
		t.Error("ожидалась ошибка для некорректного base64")
	}
}

func TestParseAddrs(t *testing.T) {
	got, err := parseAddrs("172.16.0.2", "", "2606:4700::1")
	if err != nil {
		t.Fatalf("parseAddrs: %v", err)
	}
	if len(got) != 2 || got[0].String() != "172.16.0.2" || got[1].String() != "2606:4700::1" {
		t.Errorf("parseAddrs = %v", got)
	}
	if _, err := parseAddrs("not-an-ip"); err == nil {
		t.Error("ожидалась ошибка для невалидного адреса")
	}
}

func TestEndpointAddr(t *testing.T) {
	if got := EndpointAddr(netip.MustParseAddr("162.159.192.1"), 2408); got != "162.159.192.1:2408" {
		t.Errorf("EndpointAddr v4 = %q", got)
	}
	if got := EndpointAddr(netip.MustParseAddr("2606:4700::1"), 500); got != "[2606:4700::1]:500" {
		t.Errorf("EndpointAddr v6 = %q", got)
	}
}

func TestTeardown(t *testing.T) {
	// Хвост из трёх пропусков — срез.
	if !teardown([]bool{true, true, true, false, false, false}) {
		t.Error("хвост из трёх пропусков должен считаться срезом")
	}
	// Восстановление в конце — не срез.
	if teardown([]bool{true, false, false, false, true}) {
		t.Error("восстановление в конце — не срез")
	}
	// Слишком короткая серия.
	if teardown([]bool{false, false}) {
		t.Error("серия короче tornTailFails не должна считаться срезом")
	}
}

func TestLossFraction(t *testing.T) {
	if got := lossFraction(5, 10); got != 0.5 {
		t.Errorf("lossFraction(5,10) = %v, want 0.5", got)
	}
	if got := lossFraction(0, 0); got != 0 {
		t.Errorf("lossFraction(0,0) = %v, want 0", got)
	}
}

func TestPingSummary(t *testing.T) {
	if got := pingSummary(100*time.Millisecond, 4); got != 25*time.Millisecond {
		t.Errorf("pingSummary = %v, want 25ms", got)
	}
	if got := pingSummary(100*time.Millisecond, 0); got != 0 {
		t.Errorf("pingSummary без ответов = %v, want 0", got)
	}
}

func TestBaseUAPI(t *testing.T) {
	priv := base64.StdEncoding.EncodeToString(make([]byte, 32))
	out, err := baseUAPI(Options{PrivateKey: priv, Jc: 4, Jmin: 40, Jmax: 70, I1: "<b 0x01>"})
	if err != nil {
		t.Fatalf("baseUAPI: %v", err)
	}
	for _, want := range []string{"private_key=", "jc=4", "jmin=40", "jmax=70", "i1=<b 0x01>"} {
		if !strings.Contains(out, want) {
			t.Errorf("baseUAPI не содержит %q:\n%s", want, out)
		}
	}
}
