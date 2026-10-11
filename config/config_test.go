package config

import (
	"net"
	"strings"
	"testing"
)

func TestNewConfigWARPInvariants(t *testing.T) {
	c := NewConfig()

	if c.MTU != 1280 {
		t.Errorf("MTU = %d, want 1280", c.MTU)
	}
	if c.PersistentKeepalive != 25 {
		t.Errorf("PersistentKeepalive = %d, want 25", c.PersistentKeepalive)
	}
	if c.S1 != 0 || c.S2 != 0 || c.S3 != 0 || c.S4 != 0 {
		t.Errorf("S1..S4 = %d,%d,%d,%d, want 0,0,0,0", c.S1, c.S2, c.S3, c.S4)
	}
	if c.H1 != 1 || c.H2 != 2 || c.H3 != 3 || c.H4 != 4 {
		t.Errorf("H1..H4 = %d,%d,%d,%d, want 1,2,3,4", c.H1, c.H2, c.H3, c.H4)
	}
	if c.I1 == "" {
		t.Error("I1 не должен быть пустым")
	}
	if len(c.AllowedIPs) == 0 {
		t.Error("AllowedIPs не заданы")
	}
	if err := c.Validate(); err == nil {
		t.Error("свежий конфиг без ключей должен не проходить Validate")
	}
}

func sampleConfig() Config {
	c := NewConfig()
	c.PrivateKey = "privkey=="
	c.SetAddresses("172.16.0.2", "2606:4700:110:8e1a:2d7f:57b7:9cc5:de98")
	c.DNS = "1.1.1.1, 8.8.8.8"
	c.ServerPublicKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	c.Endpoint = "162.159.192.1:2408"
	c.I1 = "<r 2><b 0xdeadbeef>"
	return c
}

func TestGenerateEmitsAllWARPSafeFields(t *testing.T) {
	c := sampleConfig()
	out, err := c.GenerateString()
	if err != nil {
		t.Fatalf("GenerateString: %v", err)
	}

	mustContain := []string{
		"[Interface]",
		"PrivateKey = privkey==",
		"Address = 172.16.0.2, 2606:4700:110:8e1a:2d7f:57b7:9cc5:de98",
		"DNS = 1.1.1.1, 8.8.8.8",
		"MTU = 1280",
		"S1 = 0",
		"S2 = 0",
		"S3 = 0",
		"S4 = 0",
		"H1 = 1",
		"H2 = 2",
		"H3 = 3",
		"H4 = 4",
		"I1 = <r 2><b 0xdeadbeef>",
		"[Peer]",
		"PublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		"AllowedIPs = 0.0.0.0/0, ::/0",
		"Endpoint = 162.159.192.1:2408",
		"PersistentKeepalive = 25",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("конфиг не содержит %q\n---\n%s", want, out)
		}
	}
}

func TestPersistentKeepaliveBelongsToPeerSection(t *testing.T) {
	c := sampleConfig()
	out, err := c.GenerateString()
	if err != nil {
		t.Fatalf("GenerateString: %v", err)
	}
	peerIdx := strings.Index(out, "[Peer]")
	kaIdx := strings.Index(out, "PersistentKeepalive = 25")
	if peerIdx < 0 || kaIdx < 0 {
		t.Fatal("не найдены [Peer] или PersistentKeepalive")
	}
	if kaIdx < peerIdx {
		t.Errorf("PersistentKeepalive должен быть в [Peer], а он до него:\n%s", out)
	}
}

func TestGenerateRejectsBrokenInvariants(t *testing.T) {
	c := sampleConfig()
	c.S1 = 5
	if _, err := c.Generate(); err == nil {
		t.Error("Generate должен отказать при S1 != 0")
	}

	c = sampleConfig()
	c.H3 = 9
	if _, err := c.Generate(); err == nil {
		t.Error("Generate должен отказать при H3 != 3")
	}

	c = sampleConfig()
	c.MTU = 1400
	if _, err := c.Generate(); err == nil {
		t.Error("Generate должен отказать при MTU != 1280")
	}
}

func TestSetDNSInvalidHostname(t *testing.T) {
	c := NewConfig()
	if err := c.SetDNS("nonexistent.invalid.invalid"); err == nil {
		t.Error("ожидалась ошибка резолва несуществующего hostname")
	}
}

func TestSetDNSIPs(t *testing.T) {
	c := NewConfig()
	if err := c.SetDNS("1.1.1.1", "8.8.8.8"); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	if c.DNS != "1.1.1.1, 8.8.8.8" {
		t.Errorf("DNS = %q", c.DNS)
	}
	if err := c.ValidateDNS(); err != nil {
		t.Errorf("ValidateDNS: %v", err)
	}
}

func TestValidateDNS(t *testing.T) {
	c := NewConfig()
	c.DNS = "1.1.1.1"
	if err := c.ValidateDNS(); err != nil {
		t.Errorf("ValidateDNS с 1.1.1.1 вернул ошибку: %v", err)
	}
	c.DNS = "::1"
	if err := c.ValidateDNS(); err != nil {
		t.Errorf("ValidateDNS с ::1 вернул ошибку: %v", err)
	}
	c.DNS = "not-an-ip"
	if err := c.ValidateDNS(); err == nil {
		t.Error("ValidateDNS с not-an-ip не вернул ошибку")
	}
	c.DNS = ""
	if err := c.ValidateDNS(); err == nil {
		t.Error("ValidateDNS с пустым DNS не вернул ошибку")
	}
}

func TestRandomizeJunk(t *testing.T) {
	c := NewConfig()
	c.RandomizeJunk()
	if c.Jc < 4 || c.Jc > 6 {
		t.Errorf("Jc = %d, want 4..6", c.Jc)
	}
	if c.Jmin < 10 || c.Jmin > 50 {
		t.Errorf("Jmin = %d, want 10..50", c.Jmin)
	}
	if c.Jmax <= c.Jmin {
		t.Errorf("Jmax = %d, должен быть больше Jmin = %d", c.Jmax, c.Jmin)
	}
}

func TestPresetsList(t *testing.T) {
	reports := PresetsList()
	if len(reports) != len(PresetDNS) {
		t.Fatalf("PresetsList вернул %d, want %d", len(reports), len(PresetDNS))
	}
	for _, r := range reports {
		if r.Name == "" {
			t.Error("preset с пустым именем")
		}
		if r.Error == nil && net.ParseIP(r.ResolvedIP) == nil {
			t.Errorf("preset %s: резолвленный IP %q не является валидным IP", r.Name, r.ResolvedIP)
		}
	}
}

func TestResolveDNSLiteral(t *testing.T) {
	ip, err := ResolveDNS("1.1.1.1")
	if err != nil {
		t.Fatalf("ResolveDNS 1.1.1.1: %v", err)
	}
	if ip != "1.1.1.1" {
		t.Errorf("ResolveDNS 1.1.1.1 вернул %q, want 1.1.1.1", ip)
	}
}
