package config

import "strings"

// DNSKind — тип значения DNS-пресета.
type DNSKind string

const (
	// DNSKindIP — готовый IP-адрес.
	DNSKindIP DNSKind = "ip"
	// DNSKindHostname — hostname, который резолвится перед использованием.
	DNSKindHostname DNSKind = "hostname"
	// DNSKindGroup — несколько значений через запятую (например, рекомендуемый набор).
	DNSKindGroup DNSKind = "group"
	// DNSKindProvider — DNS текущего провайдера; определяется во время запуска.
	DNSKindProvider DNSKind = "provider"
	// DNSKindCustom — значение вводит пользователь.
	DNSKindCustom DNSKind = "custom"
)

// DNSPreset — описание DNS-пресета для CLI и GUI.
//
// Список живёт в Go, чтобы GUI не дублировал значения и не расходился с CLI.
type DNSPreset struct {
	// ID — стабильный идентификатор для настроек GUI.
	ID string `json:"id"`
	// Label — короткая подпись для интерфейса.
	Label string `json:"label"`
	// Value — IP, hostname или список через запятую (для group).
	Value string `json:"value,omitempty"`
	// Kind — тип значения.
	Kind DNSKind `json:"kind"`
	// Note — пояснение для интерфейса.
	Note string `json:"note,omitempty"`
	// Default — входит ли в набор по умолчанию.
	Default bool `json:"default,omitempty"`
}

// DNSPresets — все известные пресеты в порядке показа.
//
// По умолчанию выбран рекомендуемый набор: Comss, Malw и GeoHide. Это баланс
// скорости и доступности сервисов, которые ограничивают обычные DNS.
var DNSPresets = []DNSPreset{
	{
		ID:      "recommended",
		Label:   "Рекомендуемые (Comss, Malw, GeoHide)",
		Value:   "dns.comss.one,dns.malw.link,dns.geohide.ru",
		Kind:    DNSKindGroup,
		Note:    "Обходят часть DNS-ограничений; проверяются по очереди",
		Default: true,
	},
	{
		ID:    "comss",
		Label: "Comss",
		Value: "dns.comss.one",
		Kind:  DNSKindHostname,
		Note:  "DNS-сервис Comss",
	},
	{
		ID:    "malw",
		Label: "Malw",
		Value: "dns.malw.link",
		Kind:  DNSKindHostname,
		Note:  "DNS-сервис Malw",
	},
	{
		ID:    "geohide",
		Label: "GeoHide",
		Value: "dns.geohide.ru",
		Kind:  DNSKindHostname,
		Note:  "DNS-сервис GeoHide",
	},
	{
		ID:    "cloudflare",
		Label: "Cloudflare 1.1.1.1",
		Value: "1.1.1.1",
		Kind:  DNSKindIP,
		Note:  "Быстрый публичный DNS",
	},
	{
		ID:    "google",
		Label: "Google 8.8.8.8",
		Value: "8.8.8.8",
		Kind:  DNSKindIP,
		Note:  "Публичный DNS Google",
	},
	{
		ID:    "yandex",
		Label: "Яндекс.DNS 77.88.8.8",
		Value: "77.88.8.8",
		Kind:  DNSKindIP,
		Note:  "Публичный DNS Яндекса",
	},
	{
		ID:    "provider",
		Label: "DNS провайдера (авто)",
		Kind:  DNSKindProvider,
		Note:  "Определяется по текущему подключению; в TUN может быть недоступен",
	},
	{
		ID:    "custom",
		Label: "Свой DNS",
		Kind:  DNSKindCustom,
		Note:  "Только обычный DNS: IP или hostname, не DoH/DoT",
	},
}

// DefaultDNSPresets возвращает пресеты, входящие в набор по умолчанию.
func DefaultDNSPresets() []DNSPreset {
	out := make([]DNSPreset, 0, len(DNSPresets))
	for _, p := range DNSPresets {
		if p.Default {
			out = append(out, p)
		}
	}
	return out
}

// DefaultDNSValues — плоский список значений по умолчанию (Comss, Malw, GeoHide).
func DefaultDNSValues() []string {
	var out []string
	for _, p := range DefaultDNSPresets() {
		for _, v := range splitDNSValue(p.Value) {
			out = append(out, v)
		}
	}
	return out
}

// AllDNSValues — все отдельные значения пресетов (без provider/custom).
func AllDNSValues() []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range DNSPresets {
		for _, v := range splitDNSValue(p.Value) {
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// PresetByID ищет пресет по идентификатору.
func PresetByID(id string) (DNSPreset, bool) {
	id = strings.TrimSpace(id)
	for _, p := range DNSPresets {
		if p.ID == id {
			return p, true
		}
	}
	return DNSPreset{}, false
}

// splitDNSValue разбивает значение group на отдельные элементы.
func splitDNSValue(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
