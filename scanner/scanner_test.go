package scanner

import (
	"net/netip"
	"testing"
	"time"
)

func TestParsePools(t *testing.T) {
	pools, err := ParsePools([]string{"162.159.192.0/24", "1.1.1.1", ""})
	if err != nil {
		t.Fatalf("ParsePools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("получено %d префиксов, want 2", len(pools))
	}
	if pools[0].String() != "162.159.192.0/24" {
		t.Errorf("pools[0] = %s", pools[0])
	}
	if pools[1].String() != "1.1.1.1/32" {
		t.Errorf("pools[1] = %s", pools[1])
	}
}

func TestParsePoolsInvalid(t *testing.T) {
	if _, err := ParsePools([]string{"not-a-prefix"}); err == nil {
		t.Error("ожидалась ошибка для мусора")
	}
	if _, err := ParsePools(nil); err == nil {
		t.Error("ожидалась ошибка для пустого списка")
	}
}

func TestExpand(t *testing.T) {
	pools, err := ParsePools([]string{"162.159.192.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	cands := Expand(pools, []int{500, 2408})
	// /30 = 4 адреса × 2 порта = 8 кандидатов.
	if len(cands) != 8 {
		t.Fatalf("Expand вернул %d, want 8", len(cands))
	}
	if cands[0].Endpoint() != "162.159.192.0:500" {
		t.Errorf("первый кандидат = %s", cands[0].Endpoint())
	}
	if cands[1].Endpoint() != "162.159.192.0:2408" {
		t.Errorf("второй кандидат = %s", cands[1].Endpoint())
	}
}

func TestExpandDefaultPools(t *testing.T) {
	cands := Expand(DefaultPrefixes(), DefaultPorts)
	// 256 + 256 + 1024 = 1536 адресов × 4 порта = 6144.
	if len(cands) != 1536*4 {
		t.Errorf("Expand дефолтных диапазонов = %d, want %d", len(cands), 1536*4)
	}
}

func TestClassifyRegion(t *testing.T) {
	cases := []struct {
		loc, colo string
		want      Status
	}{
		{"FI", "HEL", StatusOK},
		{"RU", "FRA", StatusBadRegion},
		{"ru", "arn", StatusBadRegion},
		{"DE", "DME", StatusExcludedColo},
		{"US", "kja", StatusExcludedColo},
		// Исключённый colo важнее региона.
		{"RU", "DME", StatusExcludedColo},
		{"RU", "led", StatusExcludedColo},
		{"NL", "AMS", StatusOK},
	}
	for _, c := range cases {
		if got := ClassifyRegion(c.loc, c.colo); got != c.want {
			t.Errorf("ClassifyRegion(%q,%q) = %v, want %v", c.loc, c.colo, got, c.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	for _, s := range []Status{StatusOK, StatusNoHandshake, StatusNoTrace, StatusBadRegion, StatusExcludedColo, StatusTorn} {
		if s.String() == "" {
			t.Errorf("статус %d без строки", s)
		}
	}
}

func TestRank(t *testing.T) {
	results := []Result{
		{Candidate: Candidate{IP: netip.MustParseAddr("1.1.1.1"), Port: 1}, Status: StatusOK, RTT: 90 * time.Millisecond, Loc: "FI", Colo: "HEL"},
		{Candidate: Candidate{IP: netip.MustParseAddr("2.2.2.2"), Port: 2}, Status: StatusOK, RTT: 30 * time.Millisecond, Loc: "SE", Colo: "ARN"},
		{Candidate: Candidate{IP: netip.MustParseAddr("3.3.3.3"), Port: 3}, Status: StatusBadRegion, RTT: 5 * time.Millisecond, Loc: "RU", Colo: "DME"},
		{Candidate: Candidate{IP: netip.MustParseAddr("4.4.4.4"), Port: 4}, Status: StatusNoHandshake},
	}
	ranked := Rank(results)
	if len(ranked) != 2 {
		t.Fatalf("Rank вернул %d, want 2 (только OK)", len(ranked))
	}
	if ranked[0].Endpoint() != "2.2.2.2:2" || ranked[1].Endpoint() != "1.1.1.1:1" {
		t.Errorf("порядок ранжирования неверный: %v, %v", ranked[0].Endpoint(), ranked[1].Endpoint())
	}
}

func TestRankEmpty(t *testing.T) {
	if got := Rank(nil); len(got) != 0 {
		t.Errorf("Rank(nil) = %v", got)
	}
}

func TestFormatTable(t *testing.T) {
	out := FormatTable([]Result{
		{Candidate: Candidate{IP: netip.MustParseAddr("1.1.1.1"), Port: 2408}, Status: StatusOK, Loc: "FI", Colo: "HEL", RTT: 42 * time.Millisecond, Loss: 0},
	})
	if out == "" {
		t.Fatal("пустая таблица")
	}
	for _, want := range []string{"ENDPOINT", "1.1.1.1:2408", "FI", "HEL", "42ms", "OK"} {
		if !contains(out, want) {
			t.Errorf("таблица не содержит %q:\n%s", want, out)
		}
	}
}

func TestSample(t *testing.T) {
	cands := Expand(DefaultPrefixes(), DefaultPorts)
	got := sample(cands, 10)
	if len(got) != 10 {
		t.Fatalf("sample = %d, want 10", len(got))
	}
	// sample с n >= len возвращает исходный срез.
	if len(sample(cands, len(cands)+5)) != len(cands) {
		t.Error("sample не должен увеличивать срез")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
