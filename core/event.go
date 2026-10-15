package core

import (
	"fmt"
	"strings"
	"time"
)

// Kind — тип события пайплайна.
type Kind string

// События пайплайна. Секретов (ключей, токенов, содержимого identity) в них нет:
// наружу уходят только публичные данные — адреса, страна, IP выхода.
const (
	// EventEndpointSelected — выбран очередной внешний endpoint.
	EventEndpointSelected Kind = "endpoint_selected"
	// EventOuterUp — userspace-туннель поднят (хендшейк прошёл).
	EventOuterUp Kind = "outer_up"
	// EventOuterChecked — трафик изнутри туннеля проверен (trace: warp=on, узел не исключён).
	EventOuterChecked Kind = "outer_checked"
	// EventForwarderUp — UDP-форвардер слушает локальный адрес.
	EventForwarderUp Kind = "forwarder_up"
	// EventPsiphonStarting — начинается попытка страны (запуск warp-plus с Psiphon).
	EventPsiphonStarting Kind = "psiphon_starting"
	// EventReady — SOCKS5 поднят и ведёт в нужную страну.
	EventReady Kind = "ready"
	// EventTunUp — системный TUN поднят, мост TUN→SOCKS работает (режим --tun).
	EventTunUp Kind = "tun_up"
	// EventRoutesApplied — маршруты (исключение endpoint'а, default через TUN,
	// blackhole IPv6) применены.
	EventRoutesApplied Kind = "routes_applied"
	// EventRoutesReverted — маршруты и DNS возвращены в исходное состояние.
	EventRoutesReverted Kind = "routes_reverted"
	// EventDNSSelected — выбран резолвер для DNS через цепочку.
	EventDNSSelected Kind = "dns_selected"
	// EventDNSFailed — резолвер не выбран или системный DNS не перенастроен
	// (возможна утечка DNS).
	EventDNSFailed Kind = "dns_failed"
	// EventDegraded — единичный сбой мониторинга, звено пока не меняем.
	EventDegraded Kind = "degraded"
	// EventFailover — смена звена: следующий endpoint или следующая страна.
	EventFailover Kind = "failover"
	// EventStopped — пайплайн остановлен; это последнее событие потока.
	EventStopped Kind = "stopped"
)

// Event — событие пайплайна. Заполняются только поля, относящиеся к типу события,
// поэтому в JSON лишние ключи не появляются.
type Event struct {
	Kind Kind      `json:"event"`
	Time time.Time `json:"time"`

	// Endpoint / Colo / Region / RTTMS — данные внешнего endpoint.
	Endpoint string `json:"endpoint,omitempty"`
	Colo     string `json:"colo,omitempty"`
	Region   string `json:"region,omitempty"`
	RTTMS    int64  `json:"rtt_ms,omitempty"`

	// Forwarder — локальный адрес UDP-форвардера (его получает warp-plus в -e).
	Forwarder string `json:"forwarder,omitempty"`
	// SOCKS — адрес SOCKS5-прокси, который поднял warp-plus.
	SOCKS string `json:"socks,omitempty"`

	// Country / IP / Org — выход через SOCKS5.
	Country string `json:"country,omitempty"`
	IP      string `json:"ip,omitempty"`
	Org     string `json:"org,omitempty"`

	// TUN / MTU — поднятый системный интерфейс (для tun_up/routes_applied).
	TUN string `json:"tun,omitempty"`
	MTU int    `json:"mtu,omitempty"`
	// Routes — сколько системных команд применили (для routes_applied).
	Routes int `json:"routes,omitempty"`
	// DNS — выбранный резолвер (для dns_selected).
	DNS string `json:"dns,omitempty"`
	// Seconds — сколько заняла подготовка цепочки (для ready).
	Seconds float64 `json:"seconds,omitempty"`

	// Reason — причина смены звена или просадки.
	Reason string `json:"reason,omitempty"`
	// Fails — сколько сбоев мониторинга подряд (для degraded).
	Fails int `json:"fails,omitempty"`
	// Err — ошибка, из-за которой пайплайн остановился (для stopped).
	Err string `json:"error,omitempty"`
}

// String — человекочитаемая строка события (для CLI).
func (e Event) String() string {
	switch e.Kind {
	case EventEndpointSelected:
		parts := []string{"endpoint " + dash(e.Endpoint)}
		if e.Colo != "" {
			parts = append(parts, "узел "+e.Colo)
		}
		if e.Region != "" {
			parts = append(parts, "регион "+e.Region)
		}
		if e.RTTMS > 0 {
			parts = append(parts, fmt.Sprintf("%dms", e.RTTMS))
		}
		return "выбран " + strings.Join(parts, ", ")
	case EventOuterUp:
		return "внешний туннель поднят: " + dash(e.Endpoint)
	case EventOuterChecked:
		return "трафик через туннель проверен (warp=on, узел " + dash(e.Colo) + ")"
	case EventForwarderUp:
		return "UDP-форвардер слушает " + dash(e.Forwarder)
	case EventPsiphonStarting:
		return "страна " + dash(e.Country) + ": запускаем warp-plus с Psiphon"
	case EventReady:
		var b strings.Builder
		fmt.Fprintf(&b, "готово: socks5://%s, страна %s, IP %s", dash(e.SOCKS), dash(e.Country), dash(e.IP))
		if e.Org != "" {
			fmt.Fprintf(&b, " (%s)", e.Org)
		}
		if e.Seconds > 0 {
			fmt.Fprintf(&b, ", подготовка %s", time.Duration(e.Seconds*float64(time.Second)).Round(time.Second))
		}
		return b.String()
	case EventTunUp:
		return fmt.Sprintf("системный туннель поднят: %s, MTU %d", dash(e.TUN), e.MTU)
	case EventRoutesApplied:
		return fmt.Sprintf("маршруты применены: %s, команд %d", dash(e.TUN), e.Routes)
	case EventRoutesReverted:
		return "маршруты и DNS сняты"
	case EventDNSSelected:
		if e.RTTMS > 0 {
			return fmt.Sprintf("DNS через цепочку: %s (%dms)", dash(e.DNS), e.RTTMS)
		}
		return "DNS через цепочку: " + dash(e.DNS)
	case EventDNSFailed:
		return "DNS не настроен: " + dash(e.Reason)
	case EventDegraded:
		return fmt.Sprintf("просадка (сбой %d подряд): %s", e.Fails, dash(e.Reason))
	case EventFailover:
		return "смена звена: " + dash(e.Reason)
	case EventStopped:
		if e.Err != "" {
			return "остановлено: " + e.Err
		}
		return "остановлено"
	default:
		return string(e.Kind)
	}
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "?"
	}
	return s
}
