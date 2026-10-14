package cli

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
)

func res(status scanner.Status, loc string) scanner.Result {
	return scanner.Result{
		Candidate: scanner.Candidate{IP: netip.MustParseAddr("162.159.192.1"), Port: 2408},
		Status:    status,
		Loc:       loc,
	}
}

func TestSummarizeStatuses(t *testing.T) {
	got := summarizeStatuses([]scanner.Result{
		res(scanner.StatusBadRegion, "RU"),
		res(scanner.StatusBadRegion, "RU"),
		res(scanner.StatusNoHandshake, ""),
	})
	if !strings.Contains(got, "регион RU — 2") {
		t.Errorf("нет счётчика RU: %q", got)
	}
	if !strings.Contains(got, "нет хендшейка — 1") {
		t.Errorf("нет счётчика хендшейка: %q", got)
	}
}

func TestAllBadRegion(t *testing.T) {
	cases := []struct {
		name    string
		results []scanner.Result
		want    bool
	}{
		{"все RU", []scanner.Result{
			res(scanner.StatusBadRegion, "RU"),
			res(scanner.StatusBadRegion, "RU"),
		}, true},
		{"RU + нет данных", []scanner.Result{
			res(scanner.StatusBadRegion, "RU"),
			res(scanner.StatusNoTrace, ""),
			res(scanner.StatusNoHandshake, ""),
		}, true},
		{"есть OK", []scanner.Result{
			res(scanner.StatusBadRegion, "RU"),
			res(scanner.StatusOK, "FI"),
		}, false},
		{"есть исключённый colo", []scanner.Result{
			res(scanner.StatusBadRegion, "RU"),
			res(scanner.StatusExcludedColo, "DE"),
		}, false},
		{"никто не дошёл до trace", []scanner.Result{
			res(scanner.StatusNoHandshake, ""),
			res(scanner.StatusNoTrace, ""),
		}, false},
		{"пусто", nil, false},
	}
	for _, c := range cases {
		if got := allBadRegion(c.results); got != c.want {
			t.Errorf("%s: allBadRegion = %v, want %v", c.name, got, c.want)
		}
	}
}
