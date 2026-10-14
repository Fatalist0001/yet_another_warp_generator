//go:build integration

// Интеграционный тест цепочки: реальный релизный бинарник warp-plus поверх внешнего
// AWG-туннеля. Запускается только явно:
//
//	WARPGEN_INTEGRATION=1 WARPGEN_CHAIN=1 go test -tags integration ./cli/ -run IntegrationChain -v
//
// Требует поднятого внешнего туннеля (warp=on в cdn-cgi/trace) и warp-plus в PATH.
package cli

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/alexgen/warpgen/chain"
)

func TestIntegrationChain(t *testing.T) {
	requireIntegration(t)
	if os.Getenv("WARPGEN_CHAIN") != "1" {
		t.Skip("установите WARPGEN_CHAIN=1, чтобы прогнать интеграционный тест цепочки")
	}
	if _, err := exec.LookPath("warp-plus"); err != nil {
		t.Skip("warp-plus не найден в PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sess, err := chain.Run(ctx, chain.Options{
		Countries:    []string{"NL"},
		CacheDir:     t.TempDir(),
		ReadyTimeout: 90 * time.Second,
		Attempts:     1,
		Strict:       true,
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	defer sess.Close()

	t.Logf("прокси %s, страна %s, ip %s, подготовка %s", sess.Bind, sess.Country, sess.IP, sess.Elapsed)
	if sess.IP == "" {
		t.Error("не получили внешний IP через прокси")
	}
}
