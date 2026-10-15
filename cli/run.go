package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
	"github.com/Fatalist0001/yet_another_warp_generator/core"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

// runRun — команда `run`: самодостаточный пайплайн без root, TUN и внешнего
// VPN-клиента. Внутри процесса поднимается userspace-туннель до Cloudflare, через
// него на внутренний WARP уходит локальный UDP-форвардер, поверх warp-plus поднимает
// Psiphon, и наружу отдаётся SOCKS5 с выходом вне РФ.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	// Справка по флагам печатается в stdout: `warpgen run --help` — рабочая
	// команда, её вывод хочется видеть без перенаправления stderr.
	fs.SetOutput(os.Stdout)
	countries := fs.String("countries", strings.Join(chain.DefaultCountries, ","), "страны через запятую (порядок = приоритет)")
	endpoints := fs.Int("endpoints", core.DefaultEndpoints, "сколько кандидатов внешних endpoint'ов использовать")
	bind := fs.String("bind", chain.DefaultBind, "адрес SOCKS5-прокси")
	listen := fs.String("listen", core.DefaultListen, "адрес UDP-форвардера (loopback, порт 0 — свободный)")
	warpEndpoint := fs.String("warp-endpoint", core.DefaultInnerEndpoint, "целевой адрес внутреннего WARP для форвардера")
	warpPlus := fs.String("warp-plus", "", "путь к бинарнику warp-plus (по умолчанию ищем в PATH и рядом)")
	cacheDir := fs.String("cache-dir", chain.DefaultCacheDir, "каталог identity warp-plus и кэша Psiphon")
	testURL := fs.String("test-url", chain.DefaultTestURL, "URL проверки связи внутри warp-plus (нужен HEAD=200)")
	readyTimeout := fs.Duration("ready-timeout", chain.DefaultReadyTimeout, "сколько ждать готовности одной попытки")
	attempts := fs.Int("attempts", chain.DefaultAttempts, "попыток на страну")
	strict := fs.Bool("strict", true, "страна должна совпасть с запрошенной (--strict=false — только «не RU»)")
	maxCand := fs.Int("max", 512, "сколько кандидатов проверять за скан")
	strictRegion := fs.Bool("strict-region", false, "жёсткий скан: отвергать endpoint'ы с регионом RU")
	account := fs.String("account", registration.DefaultAccountFile, "файл аккаунта WARP (ключи туннеля и identity)")
	jsonOut := fs.Bool("json", false, "события строками JSON (по одному объекту в строке)")
	debug := fs.Bool("debug", false, "подробный вывод (в stderr)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Справка по флагам уже напечатана — это не ошибка.
			return nil
		}
		return err
	}

	codes := splitList(*countries)
	for i := range codes {
		codes[i] = strings.ToUpper(codes[i])
	}

	var logf func(format string, args ...any)
	if *debug {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	cfg, err := core.NewConfig(core.Options{
		AccountFile:   *account,
		WarpPlus:      *warpPlus,
		CacheDir:      *cacheDir,
		Countries:     codes,
		Bind:          *bind,
		Listen:        *listen,
		InnerEndpoint: *warpEndpoint,
		Endpoints:     *endpoints,
		MaxCandidates: *maxCand,
		StrictRegion:  *strictRegion,
		TestURL:       *testURL,
		ReadyTimeout:  *readyTimeout,
		Attempts:      *attempts,
		Strict:        *strict,
		Logf:          logf,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events, err := core.Run(ctx, cfg)
	if err != nil {
		return err
	}

	printer := newEventPrinter(*jsonOut)
	stopErr := ""
	for ev := range events {
		printer.print(ev)
		if ev.Kind == core.EventReady {
			printer.hint(ev.SOCKS)
		}
		if ev.Kind == core.EventStopped && ev.Err != "" {
			stopErr = ev.Err
		}
	}
	if stopErr != "" {
		return errors.New(stopErr)
	}
	return nil
}

// eventPrinter печатает события пайплайна: читаемо или строками JSON.
type eventPrinter struct {
	json bool
	enc  *json.Encoder
}

func newEventPrinter(jsonOut bool) *eventPrinter {
	p := &eventPrinter{json: jsonOut}
	if jsonOut {
		p.enc = json.NewEncoder(os.Stdout)
	}
	return p
}

func (p *eventPrinter) print(ev core.Event) {
	if p.json {
		if err := p.enc.Encode(ev); err != nil {
			fmt.Fprintf(os.Stderr, "вывод события: %v\n", err)
		}
		return
	}
	fmt.Printf("%s %s\n", ev.Time.Format("15:04:05"), ev)
}

// hint — подсказка после готовности прокси. В JSON-режиме она уходит в stderr,
// чтобы поток stdout оставался чистым NDJSON.
func (p *eventPrinter) hint(socks string) {
	msg := fmt.Sprintf("Прокси готов: socks5h://%s — держим до SIGINT/SIGTERM (Ctrl+C).", socks)
	if p.json {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	fmt.Println(msg)
}
