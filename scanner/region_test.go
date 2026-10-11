package scanner

import "testing"

func TestRegionStatus(t *testing.T) {
	cases := []struct {
		name   string
		loc    string
		colo   string
		strict bool
		want   Status
	}{
		{"не-RU обычный режим", "FI", "HEL", false, StatusOK},
		{"не-RU strict", "FI", "HEL", true, StatusOK},
		{"RU учитывается по умолчанию", "RU", "FRA", false, StatusOK},
		{"RU отвергается в strict", "RU", "FRA", true, StatusBadRegion},
		{"исключённый colo обычный режим", "RU", "DME", false, StatusExcludedColo},
		{"исключённый colo strict", "RU", "LED", true, StatusExcludedColo},
		{"исключённый colo не-RU", "DE", "KJA", false, StatusExcludedColo},
	}
	for _, c := range cases {
		if got := regionStatus(c.loc, c.colo, c.strict); got != c.want {
			t.Errorf("%s: regionStatus(%q,%q,%v) = %v, want %v", c.name, c.loc, c.colo, c.strict, got, c.want)
		}
	}
}

func TestRUQualifiesByDefault(t *testing.T) {
	// Российский colo (не DME/LED/KJA) с loc=RU должен считаться подходящим.
	r := Result{Status: regionStatus("RU", "FRA", false), Loc: "RU", Colo: "FRA"}
	if !Qualifies(r) {
		t.Error("endpoint с loc=RU должен учитываться по умолчанию")
	}
	// Но исключённый colo — нет.
	r = Result{Status: regionStatus("RU", "DME", false), Loc: "RU", Colo: "DME"}
	if Qualifies(r) {
		t.Error("исключённый colo не должен считаться подходящим")
	}
}
