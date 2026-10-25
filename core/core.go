// Package core — самодостаточный пайплайн `warpgen run`.
//
// Одна команда без root, TUN и сторонних VPN-клиентов поднимает внешний
// AWG-туннель внутри процесса и отдаёт наружу SOCKS5. По умолчанию поверх него
// поднимается warp-plus с Psiphon (UDP внутреннего WARP идёт через локальный
// форвардер) — выход вне РФ. В режиме Direct (WarpOnly) Psiphon не запускается:
// наружу отдаётся сам WARP-туннель с обфускацией AWG.
//
// Пакет спроектирован как библиотека — CLI (а позже GUI и мобильные клиенты) это
// только оболочка. Внутри нет os.Exit, чтения stdin, печати в stdout и глобального
// состояния: остановка только по ctx, диагностика — через Config.Logf. Внешние
// зависимости (туннель, поиск endpoint, запуск warp-plus, проверка выхода, часы)
// заданы интерфейсами, поэтому автомат состояний тестируется без сети.
package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// Значения по умолчанию.
const (
	// DefaultInnerEndpoint — внутренний WARP, на который форвардер отправляет
	// пакеты warp-plus.
	DefaultInnerEndpoint = chain.DefaultEndpoint
	// DefaultListen — локальный адрес UDP-форвардера (порт 0 — свободный).
	DefaultListen = "127.0.0.1:0"
	// DefaultEndpoints — сколько кандидатов внешних endpoint брать на прогон.
	DefaultEndpoints = 3
	// DefaultHandshakeTimeout — предел на хендшейк при переборе кандидатов.
	DefaultHandshakeTimeout = 3 * time.Second
	// DefaultTraceTimeout — предел на запрос trace изнутри туннеля. Достаточно
	// щедрый, чтобы одиночная задержка пакета не выглядела сбоем.
	DefaultTraceTimeout = 10 * time.Second
	// DefaultMonitorInterval — как часто проверяем живую цепочку.
	DefaultMonitorInterval = 60 * time.Second
	// DefaultMonitorFailures — сколько проверок выхода подряд без ответа
	// геосервисов считать сломанным звеном (5 проверок ≈ 5 минут: переключение
	// не должно срабатывать от одиночных просадок сети).
	DefaultMonitorFailures = 5
	// DefaultTraceFailures — сколько подряд неудачных проверок внешнего туннеля
	// (trace изнутри) считать мёртвым туннелем.
	DefaultTraceFailures = 3
	// DefaultRUFailures — сколько подряд замеров выхода RU считать настоящим
	// возвратом в РФ: одиночный ответ геосервиса — не повод менять страну.
	DefaultRUFailures = 3
	// DefaultRetryPause — пауза перед следующей попыткой после сбоя уже
	// работавшей цепочки (в начальной фазе паузы нет: перебор ограничен).
	DefaultRetryPause = 30 * time.Second

	// eventsBuffer — размер буфера событий: пайплайн не блокируется на медленном
	// читателе, но и не теряет события при нормальной работе.
	eventsBuffer = 128
	// emitTimeout — потолок ожидания читателя для одного события.
	emitTimeout = 2 * time.Second
)

// Tunnel — userspace-туннель (реализуется *tunnel.Tunnel).
type Tunnel interface {
	// Handshake переустанавливает пир на endpoint и ждёт завершения хендшейка.
	Handshake(ctx context.Context, endpoint string, timeout time.Duration) bool
	// FetchTrace запрашивает cdn-cgi/trace изнутри туннеля.
	FetchTrace(ctx context.Context, timeout time.Duration) (tunnel.TraceResult, error)
	// DialContext открывает соединение через туннель (UDP-сессия форвардера).
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	// Close освобождает ресурсы туннеля.
	Close()
}

// TunnelFactory создаёт новый туннель: на каждую попытку нужен свой.
type TunnelFactory func() (Tunnel, error)

// Endpoint — рабочий endpoint внешнего туннеля (результат скана).
type Endpoint struct {
	// Addr — ip:port.
	Addr string
	// Loc — регион, который видит внешний сервис через туннель.
	Loc string
	// Colo — узел Cloudflare.
	Colo string
	// RTT — задержка, измеренная внутри туннеля.
	RTT time.Duration
	// SpeedMbps — скорость, измеренная через итоговый выход при первичной
	// настройке. 0 — замер не выполнялся или не удался.
	SpeedMbps float64
}

// EndpointLocator ищет рабочие endpoint'ы (скан + ранжирование по задержке).
type EndpointLocator interface {
	// Locate возвращает не больше limit подходящих endpoint'ов по возрастанию RTT.
	Locate(ctx context.Context, limit int) ([]Endpoint, error)
}

// ChainRequest — параметры запуска цепочки «WARP → Psiphon».
type ChainRequest struct {
	// Forwarder — локальный адрес UDP-форвардера: его warp-plus получает в -e.
	Forwarder string
	// Countries — страны в порядке приоритета (ISO-3166 alpha-2).
	Countries []string
	// Bind — адрес SOCKS5 для этой попытки. Пусто — адрес из настроек
	// chainRunner (обычный запуск). При первичном замере скорости каждому
	// кандидату даём отдельный порт, чтобы не ждать освобождения общего.
	Bind string
}

// ChainSession — живая цепочка с работающим SOCKS5.
type ChainSession struct {
	// SOCKS — адрес SOCKS5-прокси.
	SOCKS string
	// Country — страна выхода.
	Country string
	// IP — внешний IPv4, который увидели геосервисы.
	IP string
	// Org — организация/ASN выхода.
	Org string
	// Elapsed — сколько заняла подготовка цепочки.
	Elapsed time.Duration
	// Close завершает дочерние процессы цепочки. Может быть nil.
	Close func() error
}

// ChainRunner запускает цепочку поверх локального форвардера.
type ChainRunner interface {
	// Run поднимает цепочку и возвращает живую сессию (её нужно закрыть).
	Run(ctx context.Context, req ChainRequest) (*ChainSession, error)
}

// TUNSystem — системный туннель: TUN-устройство, мост TUN→SOCKS5, маршруты и
// DNS. Реализуется vpn.SystemTunnel; в тестах — подделкой. nil в Config.TUN
// означает обычный режим: наружу отдаётся только SOCKS5.
type TUNSystem interface {
	// Start поднимает TUN и мост на готовый SOCKS5, применяет маршруты и выбирает
	// DNS-резолвер. При ошибке сам откатывает применённое.
	Start(ctx context.Context, socks, endpoint string) (vpn.Status, error)
	// UpdateSocks переводит мост на новый SOCKS5 после смены цепочки и заново
	// выбирает DNS. Маршруты и TUN не трогает.
	UpdateSocks(ctx context.Context, socks string) (vpn.Status, error)
	// UpdateEndpoint обновляет исключение /32 для нового внешнего endpoint.
	UpdateEndpoint(ctx context.Context, endpoint string) (vpn.Status, error)
	// Stop снимает маршруты и DNS, затем останавливает мост и TUN. Идемпотентен.
	Stop(ctx context.Context) error
}

// ProbeResult — что увидели геосервисы через SOCKS5.
type ProbeResult struct {
	// Country / IP / Org — ответ первого ответившего сервиса.
	Country string
	IP      string
	Org     string
	// Services — сколько сервисов ответили.
	Services int
	// RU — хотя бы один сервис увидел российский выход.
	RU bool
}

// Prober проверяет выход через SOCKS5 (мониторинг живой цепочки).
type Prober interface {
	// Probe опрашивает геосервисы через SOCKS5:
	//   - ошибка — проверка не удалась (звено, возможно, сломано);
	//   - пустой результат без ошибки — связь через SOCKS5 есть, но страну
	//     определить не удалось (геосервисы молчат): менять звено не нужно;
	//   - живой прокси с выходом RU — res.RU.
	Probe(ctx context.Context, socks string) (ProbeResult, error)
}

// Clock — источник времени и таймеров (в тестах подменяется).
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	Ticker(d time.Duration) (<-chan time.Time, func())
}

// realClock — часы по умолчанию.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Config — параметры пайплайна. Обязательны Tunnel, Locator и Chain.
type Config struct {
	// InnerEndpoint — целевой адрес внутреннего WARP (для форвардера).
	InnerEndpoint string
	// Listen — локальный адрес UDP-форвардера (loopback, порт 0 — свободный).
	Listen string
	// Countries — страны Psiphon в порядке приоритета (в режиме Direct не нужны).
	Countries []string
	// Endpoints — сколько кандидатов внешних endpoint'ов использовать.
	Endpoints int

	// Direct — режим без Psiphon: наружу отдаётся локальный SOCKS5 поверх самого
	// внешнего WARP-туннеля (обфускация AWG сохраняется), без warp-plus, стран и
	// внутреннего WARP. Выход при этом российский: так Cloudflare видит клиента
	// из РФ, а задача режима — приватность канала, а не смена страны.
	Direct bool
	// Bind — адрес локального SOCKS5 в режиме Direct (только loopback).
	Bind string

	// Tunnel / Locator / Chain / Prober / Clock — внешние зависимости.
	Tunnel  TunnelFactory
	Locator EndpointLocator
	Chain   ChainRunner
	Prober  Prober
	Clock   Clock

	// ProfilePath — файл кэша выбранных endpoint'ов. Пусто — кэш выключен:
	// каждый запуск ищет endpoint'ы заново и не измеряет скорость.
	//
	// Если файла нет (первый запуск или «Пересоздать конфигурацию»), пайплайн
	// сканирует endpoint'ы, измеряет скорость через итоговый выход и сохраняет
	// лучшие. Следующие запуски берут готовый список и не тратят время на скан.
	ProfilePath string
	// SpeedTest — замер скорости через SOCKS5. Вызывается при первичной
	// настройке; nil — стандартный замер по SpeedURL/SpeedBytes/SpeedTimeout.
	SpeedTest func(ctx context.Context, socks, endpoint string) (SpeedResult, error)
	// SpeedURL / SpeedBytes / SpeedTimeout — параметры стандартного замера.
	SpeedURL     string
	SpeedBytes   int64
	SpeedTimeout time.Duration

	// TUN — фабрикатор системного туннеля (режим --tun). nil — только SOCKS5.
	// Вызывается один раз, после первого ready: маршруты применяются только
	// тогда, когда цепочка уже работает.
	TUN func() (TUNSystem, error)

	// Preflight — локальные проверки до старта (аккаунт, identity warp-plus,
	// бинарник warp-plus). Сетевых запросов делать не должен. nil — нет проверок.
	Preflight func(ctx context.Context) error
	// Logf — приёмник служебных сообщений (oversize и т.п.). nil — молча.
	Logf func(format string, args ...any)

	// MTU — MTU внешнего туннеля (пакеты больше считаем как oversize).
	MTU int
	// HandshakeTimeout / TraceTimeout — таймауты проверок туннеля.
	HandshakeTimeout time.Duration
	TraceTimeout     time.Duration
	// SessionTTL / MaxSessions — параметры сессий форвардера.
	SessionTTL  time.Duration
	MaxSessions int
	// MonitorInterval / MonitorFailures — параметры мониторинга: как часто
	// проверяем цепочку и сколько проверок выхода подряд без ответа считать
	// сломанным звеном.
	MonitorInterval time.Duration
	MonitorFailures int
	// TraceFailures — сколько подряд неудач trace через туннель считать мёртвым
	// внешним туннелем.
	TraceFailures int
	// RUFailures — сколько подряд замеров выхода RU считать сменой страны
	// (в режиме Direct не применяется).
	RUFailures int
	// RetryPause — пауза перед попыткой после сбоя работавшей цепочки.
	RetryPause time.Duration
}

// withDefaults дополняет конфигурацию значениями по умолчанию.
func (c Config) withDefaults() Config {
	if c.InnerEndpoint == "" {
		c.InnerEndpoint = DefaultInnerEndpoint
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Endpoints <= 0 {
		c.Endpoints = DefaultEndpoints
	}
	if c.Direct && strings.TrimSpace(c.Bind) == "" {
		c.Bind = DefaultSocksListen
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.SpeedURL == "" {
		c.SpeedURL = DefaultSpeedURL
	}
	if c.SpeedBytes <= 0 {
		c.SpeedBytes = DefaultSpeedBytes
	}
	if c.SpeedTimeout <= 0 {
		c.SpeedTimeout = DefaultSpeedTimeout
	}
	if c.SpeedTest == nil {
		url, bytes, timeout := c.SpeedURL, c.SpeedBytes, c.SpeedTimeout
		c.SpeedTest = func(ctx context.Context, socks, endpoint string) (SpeedResult, error) {
			return SpeedTestThroughSocks(ctx, socks, endpoint, url, bytes, timeout)
		}
	}
	if c.MTU <= 0 {
		c.MTU = DefaultMTU
	}
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.TraceTimeout <= 0 {
		c.TraceTimeout = DefaultTraceTimeout
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = DefaultSessionTTL
	}
	if c.MaxSessions <= 0 {
		c.MaxSessions = DefaultMaxSessions
	}
	if c.MonitorInterval <= 0 {
		c.MonitorInterval = DefaultMonitorInterval
	}
	if c.MonitorFailures <= 0 {
		c.MonitorFailures = DefaultMonitorFailures
	}
	if c.TraceFailures <= 0 {
		c.TraceFailures = DefaultTraceFailures
	}
	if c.RUFailures <= 0 {
		c.RUFailures = DefaultRUFailures
	}
	if c.RetryPause <= 0 {
		c.RetryPause = DefaultRetryPause
	}
	return c
}

// validate проверяет, что заданы обязательные зависимости. В режиме Direct
// (без Psiphon) список стран и запуск цепочки не нужны: наружу отдаётся SOCKS5
// поверх самого туннеля.
func (c Config) validate() error {
	switch {
	case len(c.Countries) == 0 && !c.Direct:
		return errors.New("core: не задан список стран (--countries)")
	case c.Tunnel == nil:
		return errors.New("core: не задан фабрикатор туннеля (Tunnel)")
	case c.Locator == nil:
		return errors.New("core: не задан поиск endpoint (Locator)")
	case c.Chain == nil && !c.Direct:
		return errors.New("core: не задан запуск цепочки (Chain)")
	}
	if strings.TrimSpace(c.InnerEndpoint) == "" {
		return errors.New("core: не задан целевой адрес внутреннего WARP (InnerEndpoint)")
	}
	return nil
}

// Run запускает пайплайн и сразу возвращает поток событий (канал закрывается
// вместе с пайплайном; последнее событие — stopped).
//
// Ошибка возвращается только до старта: неполная конфигурация или провал
// preflight (локальные проверки). Дальнейшие сбои видны в событиях, а их причина
// для остановки — в поле Err последнего события.
func Run(ctx context.Context, cfg Config) (<-chan Event, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Preflight != nil {
		if err := cfg.Preflight(ctx); err != nil {
			return nil, err
		}
	}

	r := &runner{cfg: cfg, clock: cfg.Clock, events: make(chan Event, eventsBuffer)}
	go r.loop(ctx)
	return r.events, nil
}
