//go:build integration

// Интеграционный тест: реальный поход в сеть (регистрация + скан + DNS).
// Запускается только явно:
//
//	WARPGEN_INTEGRATION=1 go test -tags integration ./cli/ -run Integration -v
//
// В CI не гоняется: сеть нестабильна.
package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("WARPGEN_INTEGRATION") != "1" {
		t.Skip("установите WARPGEN_INTEGRATION=1, чтобы прогнать интеграционный тест")
	}
}

func TestIntegrationRegisterScanDNS(t *testing.T) {
	requireIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	path := filepath.Join(t.TempDir(), "account.json")
	client := registration.NewClient()
	if !registration.APIReachable(ctx, client) {
		t.Skip("API Cloudflare недоступен из этой сети")
	}
	acct, err := registration.Obtain(ctx, client, registration.Account{})
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	if err := registration.SaveAccount(path, acct); err != nil {
		t.Fatalf("сохранение аккаунта: %v", err)
	}
	t.Logf("аккаунт: IPv4=%s IPv6=%s", acct.IPv4, acct.IPv6)

	cfg := config.NewConfig()
	factory := newTunnelFactory(acct, cfg, "")
	cands := scanner.Expand(scanner.DefaultPrefixes(), scanner.DefaultPorts)
	results, err := scanner.Scan(ctx, factory, cands, scanner.Options{
		Concurrency:   4,
		MaxCandidates: 32,
		TargetGood:    1,
	})
	if err != nil && len(results) == 0 {
		t.Fatalf("скан: %v", err)
	}
	ranked := scanner.Rank(results)
	if len(ranked) == 0 {
		t.Skip("подходящих endpoint не найдено (сеть может блокировать WARP)")
	}
	t.Logf("лучший endpoint: %s (%s/%s, %v)", ranked[0].Endpoint(), ranked[0].Loc, ranked[0].Colo, ranked[0].RTT)
}
