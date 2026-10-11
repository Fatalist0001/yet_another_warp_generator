// Package config — генератор .conf для AmneziaWG (WARP-safe AWG 2.0).
//
// WARP-safe инварианты (согласованы в чате и зафиксированы в refconf/):
//   - S1..S4 = 0
//   - H1..H4 = 1, 2, 3, 4
//   - MTU = 1280
//   - PersistentKeepalive = 25 (в секции [Peer])
//   - I1 — строго заглавное имя поля, значение маскирует трафик под QUIC
//   - Порядок полей в [Interface] и [Peer] — как в эталонных WARP-конфигах
//
// Значения S/H менять нельзя: это ломает связь с неизменённым сервером Cloudflare.
// Junk-пакеты (Jc/Jmin/Jmax) и I1 безопасны — обычный сервер их отбрасывает.
package config

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"strings"
)

// PresetDNS — известные DNS-пресеты из существующих генераторов.
// Список упорядочен: так же он выводится в CLI.
var PresetDNS = []string{
	"1.1.1.1",
	"8.8.8.8",
	"dns.malw.link",
	"xbox-dns.ru",
	"geohide.ru",
	"dns.comss.one",
}

// IsPresetDNS reports whether name is a known DNS preset.
func IsPresetDNS(name string) bool {
	for _, p := range PresetDNS {
		if p == name {
			return true
		}
	}
	return false
}

// ResolveDNS резолвит hostname (или IP) в IPv4-адрес. WireGuard принимает в
// `DNS =` только IP, поэтому хостнеймы резолвим заранее.
func ResolveDNS(hostname string) (string, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.LookupHost(hostname)
	if err != nil {
		return "", fmt.Errorf("резолв dns %s: %w", hostname, err)
	}
	for _, ip := range ips {
		if ip4 := net.ParseIP(ip).To4(); ip4 != nil {
			return ip4.String(), nil
		}
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("резолв dns %s: нет адресов", hostname)
	}
	// IPv6 fallback: некоторые клиенты понимают IPv6 в DNS.
	return ips[0], nil
}

// Config — результат генерации .conf.
type Config struct {
	// PrivateKey — приватный ключ WireGuard (base64).
	PrivateKey string
	// AddressV4 — IPv4-адрес интерфейса (например 172.16.0.2).
	AddressV4 string
	// AddressV6 — IPv6-адрес интерфейса (из аккаунта WARP).
	AddressV6 string
	// DNS — DNS-сервер(ы) в виде IP, разделённые ", ".
	DNS string
	// ServerPublicKey — публичный ключ сервера Cloudflare.
	ServerPublicKey string
	// Endpoint — endpoint в формате host:port.
	Endpoint string
	// AllowedIPs — разрешённые IP (обычно 0.0.0.0/0, ::/0).
	AllowedIPs []string
	// MTU — MTU интерфейса (для WARP = 1280).
	MTU int
	// PersistentKeepalive — PersistentKeepalive (для WARP = 25), в [Peer].
	PersistentKeepalive int
	// Jc / Jmin / Jmax — параметры junk-пакетов (AmneziaWG).
	Jc   int
	Jmin int
	Jmax int
	// S1..S4 — для WARP = 0.
	S1 int
	S2 int
	S3 int
	S4 int
	// H1..H4 — для WARP = 1, 2, 3, 4.
	H1 int
	H2 int
	H3 int
	H4 int
	// I1 — строго заглавное поле, маскировка под QUIC.
	I1 string

	// Опциональные поля AWG 2.0 (вариант эталонного конфига). Пустое значение
	// означает «не выводить строку».
	ContentPaddingAddition string
	RekeyAfterTime         string
	RekeyTimeout           string
	RejectAfterTime        string
	KeepaliveTimeout       string
	MaxHandshakeAttempts   string
	RandomTrailers         bool
	DisableCookies         bool
}

// NewConfig — создание нового экземпляра Config с настройками WARP-safe.
func NewConfig() Config {
	return Config{
		MTU:                 1280,
		PersistentKeepalive: 25,
		Jc:                  4,
		Jmin:                40,
		Jmax:                70,
		S1:                  0,
		S2:                  0,
		S3:                  0,
		S4:                  0,
		H1:                  1,
		H2:                  2,
		H3:                  3,
		H4:                  4,
		I1:                  DefaultI1(),
		AllowedIPs:          []string{"0.0.0.0/0", "::/0"},
	}
}

// SetKeys — установка пары ключей клиента (не выводятся в конфиг, кроме приватного).
func (c *Config) SetKeys(privateKey string) {
	c.PrivateKey = privateKey
}

// SetServer — публичный ключ сервера Cloudflare.
func (c *Config) SetServerPublicKey(pubKey string) {
	c.ServerPublicKey = pubKey
}

// SetEndpoint — установка endpoint.
func (c *Config) SetEndpoint(endpoint string) {
	c.Endpoint = endpoint
}

// SetAddresses — установка IPv4 и IPv6 адресов интерфейса.
func (c *Config) SetAddresses(v4, v6 string) {
	c.AddressV4 = strings.TrimSpace(v4)
	c.AddressV6 = strings.TrimSpace(v6)
}

// SetDNS — установка DNS-серверов (пресеты или произвольные IP/hostname).
// Резолвит hostname'ы заранее и подставляет IP.
func (c *Config) SetDNS(names ...string) error {
	var ips []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		ip, err := ResolveDNS(name)
		if err != nil {
			return fmt.Errorf("dns %s: %w", name, err)
		}
		ips = append(ips, ip)
	}
	c.DNS = strings.Join(ips, ", ")
	return nil
}

// SetJunk — установка параметров junk-пакетов Jc / Jmin / Jmax.
func (c *Config) SetJunk(jc, jmin, jmax int) {
	c.Jc = jc
	c.Jmin = jmin
	c.Jmax = jmax
}

// RandomizeJunk — рандомизация junk-параметров, чтобы не попадать в сигнатуры.
func (c *Config) RandomizeJunk() {
	c.Jc = randRange(4, 6)
	c.Jmin = randRange(10, 50)
	c.Jmax = c.Jmin + randRange(20, 100)
}

// SetI1 — установка I1 (строго заглавное поле).
func (c *Config) SetI1(i1 string) {
	c.I1 = i1
}

// AddressLine — склеивает IPv4 и IPv6 в строку для `Address =`.
func (c Config) AddressLine() string {
	switch {
	case c.AddressV4 != "" && c.AddressV6 != "":
		return c.AddressV4 + ", " + c.AddressV6
	case c.AddressV4 != "":
		return c.AddressV4
	default:
		return c.AddressV6
	}
}

// Generate — генерация .conf в формате AmneziaWG (WARP-safe AWG 2.0).
// Порядок полей повторяет эталонные конфиги из refconf/.
func (c Config) Generate() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "[Interface]\n")
	fmt.Fprintf(&buf, "PrivateKey = %s\n", c.PrivateKey)
	fmt.Fprintf(&buf, "Address = %s\n", c.AddressLine())
	if c.DNS != "" {
		fmt.Fprintf(&buf, "DNS = %s\n", c.DNS)
	}
	fmt.Fprintf(&buf, "MTU = %d\n", c.MTU)

	// S1..S4 и H1..H4 выводим всегда: они обязательны в WARP-safe конфиге.
	fmt.Fprintf(&buf, "S1 = %d\n", c.S1)
	fmt.Fprintf(&buf, "S2 = %d\n", c.S2)
	fmt.Fprintf(&buf, "S3 = %d\n", c.S3)
	fmt.Fprintf(&buf, "S4 = %d\n", c.S4)

	fmt.Fprintf(&buf, "Jc = %d\n", c.Jc)
	fmt.Fprintf(&buf, "Jmin = %d\n", c.Jmin)
	fmt.Fprintf(&buf, "Jmax = %d\n", c.Jmax)

	fmt.Fprintf(&buf, "H1 = %d\n", c.H1)
	fmt.Fprintf(&buf, "H2 = %d\n", c.H2)
	fmt.Fprintf(&buf, "H3 = %d\n", c.H3)
	fmt.Fprintf(&buf, "H4 = %d\n", c.H4)

	if c.I1 != "" {
		fmt.Fprintf(&buf, "I1 = %s\n", c.I1)
	}

	// Опциональные поля AWG 2.0.
	writeOptional(&buf, "ContentPaddingAddition", c.ContentPaddingAddition)
	writeOptional(&buf, "RekeyAfterTime", c.RekeyAfterTime)
	writeOptional(&buf, "RekeyTimeout", c.RekeyTimeout)
	writeOptional(&buf, "RejectAfterTime", c.RejectAfterTime)
	writeOptional(&buf, "KeepaliveTimeout", c.KeepaliveTimeout)
	writeOptional(&buf, "MaxHandshakeAttempts", c.MaxHandshakeAttempts)
	if c.RandomTrailers {
		fmt.Fprintf(&buf, "RandomTrailers = on\n")
	}
	if c.DisableCookies {
		fmt.Fprintf(&buf, "DisableCookies = on\n")
	}

	fmt.Fprintf(&buf, "\n[Peer]\n")
	fmt.Fprintf(&buf, "PublicKey = %s\n", c.ServerPublicKey)
	fmt.Fprintf(&buf, "AllowedIPs = %s\n", strings.Join(c.AllowedIPs, ", "))
	fmt.Fprintf(&buf, "Endpoint = %s\n", c.Endpoint)
	fmt.Fprintf(&buf, "PersistentKeepalive = %d\n", c.PersistentKeepalive)

	return buf.Bytes(), nil
}

func writeOptional(buf *bytes.Buffer, key, val string) {
	if strings.TrimSpace(val) != "" {
		fmt.Fprintf(buf, "%s = %s\n", key, val)
	}
}

// GenerateString — удобство для вывода в stdout или записи в файл.
func (c Config) GenerateString() (string, error) {
	data, err := c.Generate()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Errors возвращает список нарушений WARP-safe инвариантов (пустой — конфиг корректен).
func (c Config) Errors() []string {
	var errs []string
	if c.PrivateKey == "" {
		errs = append(errs, "PrivateKey не задан")
	}
	if c.ServerPublicKey == "" {
		errs = append(errs, "ServerPublicKey не задан")
	}
	if c.Endpoint == "" {
		errs = append(errs, "Endpoint не задан")
	}
	if c.AddressLine() == "" {
		errs = append(errs, "Address не задан")
	}
	if c.S1 != 0 || c.S2 != 0 || c.S3 != 0 || c.S4 != 0 {
		errs = append(errs, "S1..S4 должны быть 0 (иначе ломается связь с Cloudflare)")
	}
	if c.H1 != 1 || c.H2 != 2 || c.H3 != 3 || c.H4 != 4 {
		errs = append(errs, "H1..H4 должны быть 1,2,3,4 (иначе ломается связь с Cloudflare)")
	}
	if c.MTU != 1280 {
		errs = append(errs, fmt.Sprintf("MTU должен быть 1280, получено %d", c.MTU))
	}
	if c.PersistentKeepalive != 25 {
		errs = append(errs, fmt.Sprintf("PersistentKeepalive должен быть 25, получено %d", c.PersistentKeepalive))
	}
	if c.DNS != "" {
		for _, part := range strings.Split(c.DNS, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if net.ParseIP(part) == nil {
				errs = append(errs, fmt.Sprintf("DNS %q не является валидным IP", part))
			}
		}
	}
	return errs
}

// Validate — проверка WARP-safe инвариантов конфига.
func (c Config) Validate() error {
	if errs := c.Errors(); len(errs) > 0 {
		return fmt.Errorf("некорректный конфиг: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ValidateDNS — проверка, что DNS задан и каждый элемент — валидный IP.
func (c Config) ValidateDNS() error {
	if strings.TrimSpace(c.DNS) == "" {
		return fmt.Errorf("dns не задан")
	}
	for _, part := range strings.Split(c.DNS, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if net.ParseIP(part) == nil {
			return fmt.Errorf("dns не является валидным IP: %s", part)
		}
	}
	return nil
}

// PresetReport — отчёт о доступных DNS-пресетах.
type PresetReport struct {
	// Name — имя пресета.
	Name string
	// ResolvedIP — резолвленный IP.
	ResolvedIP string
	// Error — ошибка резолва, если есть.
	Error error
}

// PresetsList — список пресетов с разрешёнными IP (в стабильном порядке).
func PresetsList() []PresetReport {
	reports := make([]PresetReport, 0, len(PresetDNS))
	for _, name := range PresetDNS {
		ip, err := ResolveDNS(name)
		reports = append(reports, PresetReport{Name: name, ResolvedIP: ip, Error: err})
	}
	return reports
}

// SortedPresets возвращает копию списка пресетов, отсортированную по алфавиту.
func SortedPresets() []string {
	out := append([]string(nil), PresetDNS...)
	sort.Strings(out)
	return out
}
