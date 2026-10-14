package cli

import (
	"testing"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

func TestSplitList(t *testing.T) {
	got := splitList(" 1.1.1.1 , 8.8.8.8 ,, ")
	if len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "8.8.8.8" {
		t.Errorf("splitList = %v", got)
	}
	if len(splitList("")) != 0 {
		t.Error("пустой список должен дать пустой срез")
	}
}

func TestParsePorts(t *testing.T) {
	got, err := parsePorts("500,1701,4500,2408")
	if err != nil {
		t.Fatalf("parsePorts: %v", err)
	}
	if len(got) != 4 || got[0] != 500 || got[3] != 2408 {
		t.Errorf("parsePorts = %v", got)
	}
}

func TestParsePortsInvalid(t *testing.T) {
	for _, bad := range []string{"", "abc", "0", "70000"} {
		if _, err := parsePorts(bad); err == nil {
			t.Errorf("parsePorts(%q) должен вернуть ошибку", bad)
		}
	}
}

func TestJoinInts(t *testing.T) {
	if got := joinInts([]int{500, 1701}); got != "500,1701" {
		t.Errorf("joinInts = %q", got)
	}
}

func TestTunnelOptions(t *testing.T) {
	acct := registration.Account{
		PrivateKey: "priv==", PeerPublicKey: "peer==",
		IPv4: "172.16.0.2", IPv6: "2606:4700::1",
	}
	cfg := config.NewConfig()
	opts := tunnelOptions(acct, cfg, "eth0")
	if opts.PrivateKey != "priv==" || opts.PeerPublicKey != "peer==" {
		t.Errorf("ключи перенесены неверно: %+v", opts)
	}
	if opts.LocalV4 != "172.16.0.2" || opts.LocalV6 != "2606:4700::1" {
		t.Errorf("адреса перенесены неверно: %+v", opts)
	}
	if opts.MTU != 1280 || opts.Interface != "eth0" {
		t.Errorf("MTU/интерфейс: %+v", opts)
	}
	if opts.I1 == "" {
		t.Error("I1 должен быть перенесён из конфига")
	}
}

func TestBuildDNSTargets(t *testing.T) {
	targets := buildDNSTargets([]string{"1.1.1.1", "8.8.8.8", "nonexistent.invalid.invalid"})
	if len(targets) != 2 {
		t.Fatalf("buildDNSTargets = %v, want 2 (нерезолвящийся пропущен)", targets)
	}
	if targets[0].Name != "1.1.1.1" || targets[0].IP != "1.1.1.1" {
		t.Errorf("targets[0] = %+v", targets[0])
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if err := Run([]string{"nope"}); err == nil {
		t.Error("неизвестная команда должна возвращать ошибку")
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	if err := Run([]string{"help"}); err != nil {
		t.Errorf("help: %v", err)
	}
	if err := Run([]string{"version"}); err != nil {
		t.Errorf("version: %v", err)
	}
	if err := Run([]string{"-h"}); err != nil {
		t.Errorf("-h: %v", err)
	}
}
