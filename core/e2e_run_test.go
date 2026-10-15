//go:build e2e

// Сетевые проверки пайплайна. В обычном прогоне (`go test ./...`) не запускаются:
// тег e2e включается вручную на свободной машине —
//
//	WARPGEN_E2E=1 go test -tags e2e ./core/ -run E2E -v
//
// Проверки трогают реальную сеть, настоящий аккаунт и релизный warp-plus, поэтому
// запускать их следует только когда машина свободна. Часть пунктов чек-листа из
// README (приём локального endpoint'а warp-plus, полный прогон, failover, чистая
// остановка) остаётся за ручным прогоном: они требуют наблюдения за внешним миром.
package core

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

func e2eEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("WARPGEN_E2E") != "1" {
		t.Skip("установите WARPGEN_E2E=1, чтобы прогнать сетевые проверки")
	}
}

// e2eAccount — аккаунт для сетевых проверок (путь — через WARPGEN_E2E_ACCOUNT).
func e2eAccount(t *testing.T) registration.Account {
	t.Helper()
	path := e2eEnv("WARPGEN_E2E_ACCOUNT", filepath.Join("..", "test-account.json"))
	acct, err := registration.LoadAccount(path)
	if err != nil {
		t.Skipf("нет аккаунта для e2e (%s): %v", path, err)
	}
	return acct
}

// e2eTunnel поднимает настоящий userspace-туннель и делает хендшейк.
func e2eTunnel(t *testing.T, ctx context.Context) *tunnel.Tunnel {
	t.Helper()
	acct := e2eAccount(t)
	cfg := config.NewConfig()
	tn, err := tunnel.New(tunnel.Options{
		PrivateKey:    acct.PrivateKey,
		PeerPublicKey: acct.PeerPublicKey,
		LocalV4:       acct.IPv4,
		LocalV6:       acct.IPv6,
		MTU:           cfg.MTU,
		Jc:            cfg.Jc,
		Jmin:          cfg.Jmin,
		Jmax:          cfg.Jmax,
		I1:            cfg.I1,
	})
	if err != nil {
		t.Fatalf("туннель: %v", err)
	}
	t.Cleanup(tn.Close)

	ep := e2eEnv("WARPGEN_E2E_ENDPOINT", DefaultInnerEndpoint)
	if !tn.Handshake(ctx, ep, 5*time.Second) {
		t.Skipf("endpoint %s не отвечает на хендшейк — проверьте сеть и запустите scan", ep)
	}
	return tn
}

// Пакет длиннее MTU внешнего туннеля: процесс не падает, счётчик oversize растёт, а
// как именно ведёт себя netstack — фиксируем в логе (пункт 2 чек-листа).
func TestE2EForwarderMTU(t *testing.T) {
	requireE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tn := e2eTunnel(t, ctx)

	f, err := NewForwarder(tn, ForwarderOptions{
		Listen: "127.0.0.1:0", Target: DefaultInnerEndpoint, MTU: DefaultMTU,
		Logf: func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("форвардер: %v", err)
	}
	go func() { _ = f.Run(ctx) }()
	defer func() { _ = f.Close() }()

	conn, err := net.Dial("udp", f.Addr())
	if err != nil {
		t.Fatalf("клиент: %v", err)
	}
	defer conn.Close()

	normal := make([]byte, DefaultMTU-80)
	oversize := make([]byte, DefaultMTU+100)
	for _, p := range [][]byte{normal, oversize} {
		if _, err := conn.Write(p); err != nil {
			t.Fatalf("отправка %d байт: %v", len(p), err)
		}
	}
	time.Sleep(2 * time.Second)

	s := f.Stats()
	t.Logf("MTU=%d: сессий %d, пакетов от клиента %d (из них oversize %d), из туннеля %d",
		DefaultMTU, s.Sessions, s.PacketsFromClient, s.Oversize, s.PacketsFromTunnel)
	if s.Oversize != 1 {
		t.Errorf("oversize = %d, ожидали 1", s.Oversize)
	}
	if s.Sessions != 1 {
		t.Errorf("сессий = %d, ожидали 1", s.Sessions)
	}
}

// Полный прогон на реальных компонентах: туннель, форвардер, warp-plus с Psiphon,
// страна по двум геосервисам (пункт 3 чек-листа).
func TestE2ERunReady(t *testing.T) {
	requireE2E(t)

	country := e2eEnv("WARPGEN_E2E_COUNTRY", "NL")
	cfg, err := NewConfig(Options{
		AccountFile:   e2eEnv("WARPGEN_E2E_ACCOUNT", filepath.Join("..", "test-account.json")),
		WarpPlus:      os.Getenv("WARPGEN_E2E_WARP_PLUS"),
		CacheDir:      e2eEnv("WARPGEN_E2E_CACHE", "warpgen-e2e"),
		Countries:     []string{country},
		Bind:          "127.0.0.1:18086",
		Listen:        DefaultListen,
		InnerEndpoint: e2eEnv("WARPGEN_E2E_ENDPOINT", DefaultInnerEndpoint),
		Endpoints:     1,
		MaxCandidates: 64,
		ReadyTimeout:  2 * time.Minute,
		Attempts:      1,
		Strict:        true,
		Logf:          func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("конфигурация: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	events, err := Run(ctx, cfg)
	if err != nil {
		t.Skipf("preflight не прошёл: %v", err)
	}

	ready := false
	for ev := range events {
		t.Logf("%s", ev)
		switch ev.Kind {
		case EventReady:
			ready = true
			if ev.Country != country {
				t.Errorf("страна выхода = %q, ожидали %q", ev.Country, country)
			}
			// Пункт 5 чек-листа: после остановки не остаётся ни процессов, ни портов.
			cancel()
		case EventStopped:
			if ev.Err != "" {
				t.Errorf("пайплайн остановился с ошибкой: %s", ev.Err)
			}
		}
	}
	if !ready {
		t.Error("не дождались ready — смотрите лог выше")
	}
}
