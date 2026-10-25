package config

import (
	"strings"
	"testing"
)

func TestDefaultDNSPresetsOrder(t *testing.T) {
	got := DefaultDNSValues()
	want := []string{"dns.comss.one", "dns.malw.link", "dns.geohide.ru"}
	if len(got) != len(want) {
		t.Fatalf("DefaultDNSValues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DefaultDNSValues[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDNSPresetsHaveUniqueIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DNSPresets {
		if p.ID == "" {
			t.Error("пресет без ID")
		}
		if seen[p.ID] {
			t.Errorf("повторяющийся ID %q", p.ID)
		}
		seen[p.ID] = true
		if p.Kind == DNSKindProvider || p.Kind == DNSKindCustom {
			continue
		}
		if strings.TrimSpace(p.Value) == "" {
			t.Errorf("у пресета %q пустое значение", p.ID)
		}
	}
}

func TestPresetByID(t *testing.T) {
	p, ok := PresetByID("provider")
	if !ok || p.Kind != DNSKindProvider {
		t.Fatalf("PresetByID(provider) = %+v, %v", p, ok)
	}
	if _, ok := PresetByID("nope"); ok {
		t.Fatal("PresetByID(nope) нашёл несуществующий пресет")
	}
}

func TestAllDNSValuesIncludesDefaults(t *testing.T) {
	all := AllDNSValues()
	for _, want := range DefaultDNSValues() {
		found := false
		for _, got := range all {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllDNSValues не содержит %q", want)
		}
	}
}
