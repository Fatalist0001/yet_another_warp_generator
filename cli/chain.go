package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

// runChain — команда `chain`: внешний AWG-туннель → WARP → Psiphon, отдаёт SOCKS5.
func runChain(args []string) error {
	fs := flag.NewFlagSet("chain", flag.ContinueOnError)
	warpPlus := fs.String("warp-plus", "", "путь к бинарнику warp-plus (по умолчанию ищем в PATH и рядом)")
	countries := fs.String("countries", strings.Join(chain.DefaultCountries, ","), "страны через запятую (порядок = приоритет)")
	endpoint := fs.String("endpoint", chain.DefaultEndpoint, "WARP-endpoint для внутреннего туннеля")
	bind := fs.String("bind", chain.DefaultBind, "адрес SOCKS5-прокси")
	cacheDir := fs.String("cache-dir", chain.DefaultCacheDir, "каталог identity warp-plus и кэша Psiphon")
	testURL := fs.String("test-url", chain.DefaultTestURL, "URL проверки связи внутри warp-plus (нужен HEAD=200)")
	readyTimeout := fs.Duration("ready-timeout", chain.DefaultReadyTimeout, "сколько ждать готовности одной попытки")
	attempts := fs.Int("attempts", chain.DefaultAttempts, "попыток на страну")
	strict := fs.Bool("strict", true, "страна должна совпасть с запрошенной (--strict=false — только «не RU»)")
	account := fs.String("account", registration.DefaultAccountFile, "файл аккаунта WARP (источник identity для warp-plus)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	codes := splitList(*countries)
	for i := range codes {
		codes[i] = strings.ToUpper(codes[i])
	}

	opts := chain.Options{
		WarpPlus:     *warpPlus,
		Countries:    codes,
		Endpoint:     *endpoint,
		Bind:         *bind,
		CacheDir:     *cacheDir,
		TestURL:      *testURL,
		ReadyTimeout: *readyTimeout,
		Attempts:     *attempts,
		Strict:       *strict,
		AccountFile:  *account,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sess, err := chain.Run(ctx, opts)
	if err != nil {
		var fail *chain.Failure
		if errors.As(err, &fail) {
			fmt.Fprintln(os.Stderr, "Ни одна страна не прошла. Попытки:")
			fmt.Fprint(os.Stderr, chain.FormatFailures(fail.Attempts))
		}
		return err
	}
	defer sess.Close()

	fmt.Printf("SOCKS5 готов: socks5://%s\n", sess.Bind)
	fmt.Printf("  страна: %s\n", sess.Country)
	fmt.Printf("  IP: %s\n", sess.IP)
	fmt.Printf("  организация: %s\n", orDash(sess.Org))
	fmt.Printf("  подготовка: %s\n", sess.Elapsed.Round(time.Second))
	printGeoDisagreement(sess.Queries, sess.Country)
	fmt.Println("Держим прокси до SIGINT/SIGTERM (Ctrl+C).")

	<-ctx.Done()
	fmt.Println("\nОстанавливаем warp-plus…")
	return nil
}

// printGeoDisagreement печатает ответы всех сервисов, если они разошлись по стране.
func printGeoDisagreement(results []chain.GeoResult, country string) {
	if len(results) < 2 {
		return
	}
	disagree := false
	for _, r := range results {
		if r.Country != country {
			disagree = true
		}
	}
	if !disagree {
		return
	}
	fmt.Println("  сервисы расходятся по стране:")
	for _, r := range results {
		fmt.Printf("    %s: %s (%s)\n", r.Service, r.Country, orDash(r.IP))
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "?"
	}
	return s
}
