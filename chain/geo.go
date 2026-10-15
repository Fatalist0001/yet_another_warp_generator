package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// GeoService — внешний сервис, по которому проверяем выходной IP и страну.
type GeoService struct {
	Name string
	URL  string
}

// DefaultGeoServices — два независимых геосервиса: одного мало, они могут
// расходиться по стране (на замерах так было с PL: ipinfo.io — PL, ifconfig.co — FR).
var DefaultGeoServices = []GeoService{
	{Name: "ifconfig.co", URL: "https://ifconfig.co/json"},
	{Name: "ipinfo.io", URL: "https://ipinfo.io/json"},
}

// GeoResult — разобранный ответ геосервиса.
type GeoResult struct {
	// Service — имя сервиса.
	Service string
	// IP — внешний IPv4, как его видит сервис.
	IP string
	// Country — страна (ISO-3166 alpha-2).
	Country string
	// Org — организация/ASN выхода.
	Org string
}

// geoResponse — общий вид ответов ifconfig.co/json и ipinfo.io/json.
type geoResponse struct {
	IP         string `json:"ip"`
	Country    string `json:"country"`
	CountryISO string `json:"country_iso"`
	Org        string `json:"org"`
	ASNOrg     string `json:"asn_org"`
}

// Ошибки готовности.
var (
	errNoGeoService = errors.New("ни один геосервис не ответил")
	errExitRU       = errors.New("выходной регион RU")
	errWrongCountry = errors.New("страна не совпала с запрошенной")
)

// queryGeo запрашивает один геосервис через SOCKS5. Ошибка означает «сервис
// пропускаем»: 429 (rate limit), не-JSON (HTML-челлендж Cloudflare), таймаут или
// не-IPv4 адрес — это не сбой цепочки, а повод положиться на второй сервис.
func queryGeo(ctx context.Context, client *http.Client, svc GeoService) (GeoResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, geoRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, svc.URL, nil)
	if err != nil {
		return GeoResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return GeoResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return GeoResult{}, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return GeoResult{}, errors.New("429: сервис пропускаем")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GeoResult{}, fmt.Errorf("статус %d", resp.StatusCode)
	}

	var gr geoResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return GeoResult{}, errors.New("ответ не JSON (возможно, HTML-челлендж)")
	}
	ip := strings.TrimSpace(gr.IP)
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return GeoResult{}, fmt.Errorf("некорректный IPv4 %q", ip)
	}
	country := strings.ToUpper(strings.TrimSpace(firstNonEmpty(gr.CountryISO, gr.Country)))
	if len(country) != 2 {
		return GeoResult{}, errors.New("страна не определена")
	}
	return GeoResult{
		Service: svc.Name,
		IP:      ip,
		Country: country,
		Org:     strings.TrimSpace(firstNonEmpty(gr.ASNOrg, gr.Org)),
	}, nil
}

// Evaluate опрашивает геосервисы параллельно и применяет критерий успеха:
// минимум один ответил, ни один не отдал RU, и при strict — хотя бы один назвал
// запрошенную страну. Результаты возвращаются даже при ошибке (для отчёта).
//
// Экспортирован для встраивания (пакет core мониторит живую цепочку тем же
// критерием, что и готовность).
func Evaluate(ctx context.Context, client *http.Client, services []GeoService, country string, strict bool) ([]GeoResult, error) {
	results := make([]GeoResult, len(services))
	var wg sync.WaitGroup
	for i, svc := range services {
		wg.Add(1)
		go func(i int, svc GeoService) {
			defer wg.Done()
			res, err := queryGeo(ctx, client, svc)
			if err == nil {
				results[i] = res
			}
		}(i, svc)
	}
	wg.Wait()

	ok := make([]GeoResult, 0, len(results))
	for _, r := range results {
		if r.IP != "" {
			ok = append(ok, r)
		}
	}
	if len(ok) == 0 {
		return nil, errNoGeoService
	}
	for _, r := range ok {
		if r.Country == "RU" {
			return ok, errExitRU
		}
	}
	if strict {
		want := strings.ToUpper(country)
		for _, r := range ok {
			if r.Country == want {
				return ok, nil
			}
		}
		return ok, errWrongCountry
	}
	return ok, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
