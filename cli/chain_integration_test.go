//go:build integration

// Интеграционный тест цепочки: реальный релизный бинарник warp-plus поверх внешнего
// AWG-туннеля. Запускается только явно:
//
//	WARPGEN_INTEGRATION=1 WARPGEN_CHAIN=1 go test -tags integration ./cli/ -run IntegrationChain -v
//
// Требует поднятого внешнего туннеля (warp=on в cdn-cgi/trace) и warp-plus в PATH
// (или WARPGEN_CHAIN_WARP_PLUS с путём к бинарнику). Так как endpoint, на котором
// проходит внутренний хендшейк, зависит от сети, его можно переопределить через
// WARPGEN_CHAIN_ENDPOINT; файл аккаунта — через WARPGEN_CHAIN_ACCOUNT.
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestIntegrationChain(t *testing.T) {
	requireIntegration(t)
	if os.Getenv("WARPGEN_CHAIN") != "1" {
		t.Skip("установите WARPGEN_CHAIN=1, чтобы прогнать интеграционный тест цепочки")
	}
	warpPlus := os.Getenv("WARPGEN_CHAIN_WARP_PLUS")
	if warpPlus == "" {
		p, err := exec.LookPath("warp-plus")
		if err != nil {
			t.Skip("warp-plus не найден в PATH (или задайте WARPGEN_CHAIN_WARP_PLUS)")
		}
		warpPlus = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sess, err := chain.Run(ctx, chain.Options{
		WarpPlus:     warpPlus,
		Countries:    []string{"NL"},
		Endpoint:     os.Getenv("WARPGEN_CHAIN_ENDPOINT"),
		CacheDir:     t.TempDir(),
		TestURL:      envOr("WARPGEN_CHAIN_TEST_URL", chain.DefaultTestURL),
		ReadyTimeout: 120 * time.Second,
		Attempts:     1,
		Strict:       true,
		// Тест запускается из каталога пакета (./cli), поэтому путь к аккаунту
		// по умолчанию ведёт в корень репозитория.
		AccountFile: envOr("WARPGEN_CHAIN_ACCOUNT", filepath.Join("..", "test-account.json")),
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	defer sess.Close()

	t.Logf("прокси %s, страна %s, ip %s, подготовка %s", sess.Bind, sess.Country, sess.IP, sess.Elapsed)
	if sess.IP == "" {
		t.Error("не получили внешний IP через прокси")
	}
	if sess.Country != "NL" {
		t.Errorf("страна = %q, ожидали NL", sess.Country)
	}
}
