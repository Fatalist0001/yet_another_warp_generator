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
	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// runRun — команда `run`: самодостаточный пайплайн. Внутри процесса поднимается
// userspace-туннель до Cloudflare, через него на внутренний WARP уходит локальный
// UDP-форвардер, поверх warp-plus поднимает Psiphon, и наружу отдаётся SOCKS5 с
// выходом вне РФ.
//
// С --warp-only Psiphon не запускается: наружу отдаётся локальный SOCKS5 прямо
// поверх WARP-туннеля с обфускацией AWG (выход остаётся российским).
//
// Без --tun для этого не нужны root и TUN: наружу смотрит только SOCKS5.
// С --tun весь трафик машины идёт по той же цепочке через TUN-интерфейс.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	// Справка по флагам печатается в stdout: `warpgen run --help` — рабочая
	// команда, её вывод хочется видеть без перенаправления stderr.
	fs.SetOutput(os.Stdout)
	countries := fs.String("countries", strings.Join(chain.DefaultCountries, ","), "страны через запятую (порядок = приоритет; с --warp-only не нужны)")
	warpOnly := fs.Bool("warp-only", false, "без Psiphon: SOCKS5 прямо поверх WARP-туннеля (обфускация AWG сохраняется)")
	endpoints := fs.Int("endpoints", core.DefaultEndpoints, "сколько кандидатов внешних endpoint'ов использовать")
	bind := fs.String("bind", chain.DefaultBind, "адрес SOCKS5-прокси")
	listen := fs.String("listen", core.DefaultListen, "адрес UDP-форвардера (loopback, порт 0 — свободный)")
	warpEndpoint := fs.String("warp-endpoint", core.DefaultInnerEndpoint, "целевой адрес внутреннего WARP для форвардера")
	warpPlus := fs.String("warp-plus", "", "путь к бинарнику warp-plus (по умолчанию ищем в PATH и рядом)")
	cacheDir := fs.String("cache-dir", chain.DefaultCacheDir(), "каталог identity warp-plus и кэша Psiphon")
	testURL := fs.String("test-url", chain.DefaultTestURL, "URL проверки связи внутри warp-plus (нужен HEAD=200)")
	readyTimeout := fs.Duration("ready-timeout", chain.DefaultReadyTimeout, "сколько ждать готовности одной попытки")
	attempts := fs.Int("attempts", chain.DefaultAttempts, "попыток на страну")
	strict := fs.Bool("strict", true, "страна должна совпасть с запрошенной (--strict=false — только «не RU»)")
	maxCand := fs.Int("max", 512, "сколько кандидатов проверять за скан")
	strictRegion := fs.Bool("strict-region", false, "жёсткий скан: отвергать endpoint'ы с регионом RU")
	endpointCache := fs.String("endpoint-cache", "", "файл кэша выбранных endpoint'ов (первый запуск замеряет скорость, следующие берут готовые)")
	speedURL := fs.String("speed-url", core.DefaultSpeedURL, "URL файла для замера скорости при первичной настройке кэша")
	speedBytes := fs.Int64("speed-bytes", core.DefaultSpeedBytes, "сколько байт скачивать для замера скорости")
	speedTimeout := fs.Duration("speed-timeout", core.DefaultSpeedTimeout, "потолок одного замера скорости")
	monitorInterval := fs.Duration("monitor-interval", core.DefaultMonitorInterval, "как часто проверять живую цепочку")
	monitorFailures := fs.Int("monitor-failures", core.DefaultMonitorFailures, "сколько проверок выхода подряд без ответа считать сломанным звеном")
	traceFailures := fs.Int("trace-failures", core.DefaultTraceFailures, "сколько подряд неудач trace через туннель считать мёртвым туннелем")
	ruFailures := fs.Int("ru-failures", core.DefaultRUFailures, "сколько подряд замеров выхода RU считать сменой страны")
	retryPause := fs.Duration("retry-pause", core.DefaultRetryPause, "пауза перед новой попыткой после сбоя работавшей цепочки")
	account := fs.String("account", registration.DefaultAccountFile, "файл аккаунта WARP (ключи туннеля и identity)")
	tun := fs.Bool("tun", false, "системный туннель: весь трафик машины идёт по цепочке (нужен root/администратор)")
	stateDir := fs.String("state-dir", "", "каталог журнала маршрутов для tun-revert (по умолчанию — каталог состояния в профиле)")
	tunName := fs.String("tun-name", vpn.DefaultTUNName, "имя TUN-интерфейса")
	tunMTU := fs.Int("tun-mtu", vpn.DefaultTUNMTU, "MTU TUN-интерфейса")
	tunFD := fs.Int("tun-fd", -1, "готовый файловый дескриптор TUN (Android: fd от VpnService)")
	tunFDSocket := fs.String("tun-fd-socket", "", "unix-сокет, по которому получить fd TUN (Android: VpnService)")
	dns := fs.String("dns", vpn.DefaultDNS, "резолверы для DNS через цепочку (IP/hostname[:port] через запятую; в --tun hostname резолвится заранее)")
	udp := fs.String("udp", string(vpn.UDPDrop), "политика UDP: drop — не проводить (QUIC не работает: в ответ ICMP «порт недоступен», приложения уходят на TCP)")
	dryRun := fs.Bool("dry-run", false, "показать системные команды и ничего не менять (с --tun)")
	stopFile := fs.String("stop-file", "", "файл-сигнал остановки: появление файла завершает пайплайн (GUI под pkexec и на Windows)")
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

	if !*tun && (*tunFD >= 0 || strings.TrimSpace(*tunFDSocket) != "") {
		return errors.New("--tun-fd/--tun-fd-socket имеют смысл только вместе с --tun")
	}

	// Коды стран проверяем до скана endpoint'ов: неизвестный код warp-plus не
	// примет, и узнать об этом лучше сразу, а не через минуту перебора.
	if !*warpOnly {
		if err := chain.ValidateCountries(codes); err != nil {
			return err
		}
	}

	// Права проверяем до любого сетевого действия: без root --tun должен
	// упасть с понятным сообщением, не пытаясь резолвить DNS и не трогая
	// аккаунт. --dry-run ничего не меняет в системе, поэтому root не нужен.
	// Android передаёт готовый fd от VpnService: там root не нужен.
	if *tun && !*dryRun && *tunFD < 0 && strings.TrimSpace(*tunFDSocket) == "" {
		if err := vpn.CheckPrivileges(); err != nil {
			return err
		}
	}

	// TUN принимает в DNS только IP: hostname'ы (например, Comss/Malw/GeoHide)
	// резолвим заранее. Без TUN DNS не используется, поэтому лишний резолв не
	// делаем — запуск не должен зависеть от доступности DNS провайдера.
	dnsAddrs := splitList(*dns)
	if *tun && len(dnsAddrs) > 0 {
		var err error
		dnsAddrs, err = resolveDNSAddrs(dnsAddrs)
		if err != nil {
			return err
		}
	}

	var tunDevice vpn.Device
	if *tun {
		var file *os.File
		switch {
		case *tunFD >= 0:
			file = os.NewFile(uintptr(*tunFD), "tun")
		case strings.TrimSpace(*tunFDSocket) != "":
			var err error
			file, err = receiveTunFD(*tunFDSocket)
			if err != nil {
				return err
			}
		}
		if file != nil {
			dev, err := vpn.DeviceFromFile(file, *tunName, *tunMTU)
			if err != nil {
				_ = file.Close()
				return err
			}
			tunDevice = dev
		}
	}

	// Служебный вывод: --debug включает его целиком, а --dry-run — потому что его
	// обещанный вывод и есть системные команды. Печатается в stderr, поэтому поток
	// stdout остаётся чистым даже в --json.
	var logf func(format string, args ...any)
	if *debug || *dryRun {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	cfg, err := core.NewConfig(core.Options{
		WarpOnly:    *warpOnly,
		TUN:         *tun,
		TUNName:     *tunName,
		TUNMTU:      *tunMTU,
		TUNDevice:   tunDevice,
		TUNStateDir: *stateDir,
		TUNCheckPrivileges: func() error {
			if tunDevice != nil {
				return nil
			}
			return vpn.CheckPrivileges()
		},
		DNS:             dnsAddrs,
		UDP:             *udp,
		DryRun:          *dryRun,
		AccountFile:     *account,
		WarpPlus:        *warpPlus,
		CacheDir:        *cacheDir,
		Countries:       codes,
		Bind:            *bind,
		Listen:          *listen,
		InnerEndpoint:   *warpEndpoint,
		Endpoints:       *endpoints,
		MaxCandidates:   *maxCand,
		StrictRegion:    *strictRegion,
		EndpointCache:   *endpointCache,
		SpeedURL:        *speedURL,
		SpeedBytes:      *speedBytes,
		SpeedTimeout:    *speedTimeout,
		TestURL:         *testURL,
		ReadyTimeout:    *readyTimeout,
		Attempts:        *attempts,
		Strict:          *strict,
		MonitorInterval: *monitorInterval,
		MonitorFailures: *monitorFailures,
		TraceFailures:   *traceFailures,
		RUFailures:      *ruFailures,
		RetryPause:      *retryPause,
		Logf:            logf,
	})
	if err != nil {
		if tunDevice != nil {
			_ = tunDevice.Close()
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go watchStopFile(ctx, *stopFile, stop, 0)

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
