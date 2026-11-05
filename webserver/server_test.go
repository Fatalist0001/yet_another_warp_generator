package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

func testConfig() Config {
	return Config{
		Listen:              "127.0.0.1:0",
		Logger:              log.New(io.Discard, "", 0),
		RegisterLimit:       100,
		RegisterHourLimit:   100,
		RegisterGlobalLimit: 100,
		DNSLimit:            100,
		MaxInflight:         4,
		DNSCache:            time.Minute,
		DNSTimeout:          2 * time.Second,
		NowFunc:             func() time.Time { return time.Unix(1700000000, 0) },
		RegisterFunc: func(context.Context) (registration.Account, error) {
			return registration.Account{
				PrivateKey:    "private-key",
				PeerPublicKey: "peer-public-key",
				IPv4:          "172.16.0.2/32",
				IPv6:          "2606:4700:110::9/128",
			}, nil
		},
		Resolve4Func: func(_ context.Context, host string) ([]string, error) {
			if host == "dns.comss.one" {
				return []string{"9.9.9.9", "1.2.3.4", "1.2.3.4"}, nil
			}
			if net.ParseIP(host) != nil {
				return []string{host}, nil
			}
			return []string{"1.2.3.4"}, nil
		},
		ProbeDNSFunc: func(_ context.Context, ip string) ProbeResult {
			if ip == "1.2.3.4" {
				return ProbeResult{OK: true, RTT: 12 * time.Millisecond}
			}
			return ProbeResult{OK: false, Err: "таймаут"}
		},
	}
}

func newTestHandler(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.Handler()
}

func doRequest(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:12345"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeJSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON: %v (%s)", err, rr.Body.String())
	}
	return out
}

func TestStaticAndSecurityHeaders(t *testing.T) {
	h := newTestHandler(t, testConfig())

	rr := doRequest(h, http.MethodGet, "/", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("GET / не отдал index.html: %.80s", rr.Body.String())
	}
	resp := rr.Result()
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
		if resp.Header.Get(name) == "" {
			t.Errorf("нет заголовка %s", name)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP не закрывает frame/scripts: %s", csp)
	}

	font := doRequest(h, http.MethodGet, "/fonts/jbm-latin.woff2", "", nil)
	if font.Code != http.StatusOK || !strings.Contains(font.Header().Get("Content-Type"), "font/woff2") {
		t.Errorf("шрифт: %d %q", font.Code, font.Header().Get("Content-Type"))
	}
	if !strings.Contains(font.Header().Get("Cache-Control"), "max-age") {
		t.Errorf("шрифт не кэшируется: %q", font.Header().Get("Cache-Control"))
	}

	license := doRequest(h, http.MethodGet, "/fonts/OFL-JetBrainsMono.txt", "", nil)
	if license.Code != http.StatusOK || !strings.Contains(license.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("лицензия: %d %q", license.Code, license.Header().Get("Content-Type"))
	}

	head := doRequest(h, http.MethodHead, "/", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD /: %d, body=%d", head.Code, head.Body.Len())
	}

	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("нет ETag у index.html")
	}
	cached := doRequest(h, http.MethodGet, "/", "", map[string]string{"If-None-Match": etag})
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d, body=%d", cached.Code, cached.Body.Len())
	}

	missing := doRequest(h, http.MethodGet, "/no-such-file", "", nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("неизвестный путь = %d, want 404", missing.Code)
	}
	if got := decodeJSON[errorResponse](t, missing); got.Code != "NOT_FOUND" {
		t.Errorf("код 404 = %q", got.Code)
	}

	post := doRequest(h, http.MethodPost, "/", "", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", post.Code)
	}
}

func TestWarpMethodOriginAndContract(t *testing.T) {
	h := newTestHandler(t, testConfig())

	get := doRequest(h, http.MethodGet, "/api/warp", "", nil)
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET /api/warp = %d, Allow=%q", get.Code, get.Header().Get("Allow"))
	}
	if got := decodeJSON[errorResponse](t, get); got.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("код = %q", got.Code)
	}

	noJSON := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{"Content-Type": "text/plain"})
	if noJSON.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain = %d, want 415", noJSON.Code)
	}

	ok := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{"Content-Type": "application/json; charset=utf-8"})
	if ok.Code != http.StatusOK {
		t.Fatalf("POST /api/warp = %d: %s", ok.Code, ok.Body.String())
	}
	warp := decodeJSON[warpResponse](t, ok)
	if warp.PrivKey != "private-key" || warp.PeerPub != "peer-public-key" {
		t.Errorf("ключи: %+v", warp)
	}
	if warp.ClientIPv4 != "172.16.0.2" || warp.ClientIPv6 != "2606:4700:110::9" {
		t.Errorf("маски не сняты: %+v", warp)
	}
	if ok.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("ответ с ключом кэшируется: %q", ok.Header().Get("Cache-Control"))
	}

	sameOrigin := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "http://example.com",
	})
	if sameOrigin.Code != http.StatusOK {
		t.Errorf("same-origin = %d", sameOrigin.Code)
	}
	if got := sameOrigin.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Errorf("ACAO same-origin = %q", got)
	}

	evil := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "https://evil.example",
	})
	if evil.Code != http.StatusForbidden {
		t.Errorf("чужой origin = %d, want 403", evil.Code)
	}
	if got := decodeJSON[errorResponse](t, evil); got.Code != "ORIGIN_NOT_ALLOWED" {
		t.Errorf("код = %q", got.Code)
	}

	allowedCfg := testConfig()
	allowedCfg.AllowedOrigins = []string{"https://app.example"}
	hAllowed := newTestHandler(t, allowedCfg)
	allowed := doRequest(hAllowed, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "https://app.example",
	})
	if allowed.Code != http.StatusOK || allowed.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("allowlist: %d, ACAO=%q", allowed.Code, allowed.Header().Get("Access-Control-Allow-Origin"))
	}

	pre := doRequest(h, http.MethodOptions, "/api/warp", "", map[string]string{"Origin": "http://example.com"})
	if pre.Code != http.StatusNoContent || !strings.Contains(pre.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("OPTIONS: %d, methods=%q", pre.Code, pre.Header().Get("Access-Control-Allow-Methods"))
	}
	preEvil := doRequest(h, http.MethodOptions, "/api/warp", "", map[string]string{"Origin": "https://evil.example"})
	if preEvil.Code != http.StatusForbidden {
		t.Errorf("OPTIONS чужой origin = %d", preEvil.Code)
	}
}

func TestWarpRateLimitsAndBusy(t *testing.T) {
	cfg := testConfig()
	cfg.RegisterLimit = 1
	h := newTestHandler(t, cfg)
	headers := map[string]string{"Content-Type": "application/json"}

	if rr := doRequest(h, http.MethodPost, "/api/warp", "{}", headers); rr.Code != http.StatusOK {
		t.Fatalf("первая регистрация = %d", rr.Code)
	}
	limited := doRequest(h, http.MethodPost, "/api/warp", "{}", headers)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Errorf("лимит: %d, Retry-After=%q", limited.Code, limited.Header().Get("Retry-After"))
	}
	if got := decodeJSON[errorResponse](t, limited); got.Code != "RATE_LIMITED" {
		t.Errorf("код = %q", got.Code)
	}

	// Часовой лимит отдаёт Retry-After на час, а не на минуту.
	hourCfg := testConfig()
	hourCfg.RegisterLimit = 100
	hourCfg.RegisterHourLimit = 1
	hourCfg.RegisterGlobalLimit = 100
	hHour := newTestHandler(t, hourCfg)
	if rr := doRequest(hHour, http.MethodPost, "/api/warp", "{}", headers); rr.Code != http.StatusOK {
		t.Fatalf("часовая первая = %d", rr.Code)
	}
	hourLimited := doRequest(hHour, http.MethodPost, "/api/warp", "{}", headers)
	if hourLimited.Code != http.StatusTooManyRequests || hourLimited.Header().Get("Retry-After") != "3600" {
		t.Errorf("часовой лимит: %d, Retry-After=%q", hourLimited.Code, hourLimited.Header().Get("Retry-After"))
	}

	// Глобальный лимит не зависит от IP.
	globalCfg := testConfig()
	globalCfg.RegisterGlobalLimit = 1
	hGlobal := newTestHandler(t, globalCfg)
	if rr := doRequest(hGlobal, http.MethodPost, "/api/warp", "{}", headers); rr.Code != http.StatusOK {
		t.Fatalf("глобальная первая = %d", rr.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/warp", strings.NewReader("{}"))
	req.RemoteAddr = "198.51.100.7:9999"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hGlobal.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("глобальный лимит: %d", rr.Code)
	}

	tooBig := doRequest(h, http.MethodPost, "/api/warp", strings.Repeat("x", maxRequestBytes*2), headers)
	if tooBig.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("большое тело = %d, want 413", tooBig.Code)
	}
}

func TestWarpBusy(t *testing.T) {
	cfg := testConfig()
	cfg.MaxInflight = 1
	started := make(chan struct{})
	release := make(chan struct{})
	cfg.RegisterFunc = func(context.Context) (registration.Account, error) {
		close(started)
		<-release
		return registration.Account{PrivateKey: "p", PeerPublicKey: "q", IPv4: "172.16.0.2/32"}, nil
	}
	h := newTestHandler(t, cfg)
	headers := map[string]string{"Content-Type": "application/json"}

	done := make(chan int, 1)
	go func() {
		done <- doRequest(h, http.MethodPost, "/api/warp", "{}", headers).Code
	}()
	<-started

	busy := doRequest(h, http.MethodPost, "/api/warp", "{}", headers)
	if busy.Code != http.StatusServiceUnavailable {
		t.Errorf("занятый сервер = %d, want 503", busy.Code)
	}
	if got := decodeJSON[errorResponse](t, busy); got.Code != "SERVER_BUSY" {
		t.Errorf("код = %q", got.Code)
	}

	close(release)
	if code := <-done; code != http.StatusOK {
		t.Errorf("первая регистрация завершилась %d", code)
	}
}

func TestWarpErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"cloudflare 429", errors.New("POST /reg: статус 429: error code: 1015"), http.StatusTooManyRequests, "RATE_LIMITED"},
		{"timeout", fmt.Errorf("POST /reg: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.cloudflareclient.com"}, http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE"},
		{"upstream 500", errors.New("PATCH /reg/x: статус 500: oops"), http.StatusBadGateway, "REGISTRATION_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.RegisterFunc = func(context.Context) (registration.Account, error) {
				return registration.Account{}, tc.err
			}
			h := newTestHandler(t, cfg)
			rr := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{"Content-Type": "application/json"})
			if rr.Code != tc.status {
				t.Errorf("status = %d, want %d", rr.Code, tc.status)
			}
			if got := decodeJSON[errorResponse](t, rr); got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if strings.Contains(rr.Body.String(), "1015") || strings.Contains(rr.Body.String(), "oops") {
				t.Errorf("ответ раскрывает детали upstream: %s", rr.Body.String())
			}
		})
	}
}

func TestWarpIncompleteAccount(t *testing.T) {
	cfg := testConfig()
	cfg.RegisterFunc = func(context.Context) (registration.Account, error) {
		return registration.Account{PrivateKey: "p", PeerPublicKey: "q"}, nil
	}
	h := newTestHandler(t, cfg)
	rr := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{"Content-Type": "application/json"})
	if rr.Code != http.StatusBadGateway {
		t.Errorf("неполный аккаунт = %d, want 502", rr.Code)
	}
	if got := decodeJSON[errorResponse](t, rr); got.Code != "REGISTRATION_FAILED" {
		t.Errorf("код = %q", got.Code)
	}
}

func TestDNSReportAndCache(t *testing.T) {
	cfg := testConfig()
	var resolveCalls atomic.Int64
	baseResolve := cfg.Resolve4Func
	cfg.Resolve4Func = func(ctx context.Context, host string) ([]string, error) {
		resolveCalls.Add(1)
		return baseResolve(ctx, host)
	}
	h := newTestHandler(t, cfg)

	rr := doRequest(h, http.MethodGet, "/api/dns", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/dns = %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeJSON[dnsResponse](t, rr)
	if len(resp.Presets) != 7 {
		t.Fatalf("пресетов %d, want 7: %+v", len(resp.Presets), resp.Presets)
	}
	comss := findPreset(t, resp.Presets, "comss")
	if !comss.OK || len(comss.LiveIPs) != 1 || comss.LiveIPs[0] != "1.2.3.4" {
		t.Errorf("comss: %+v", comss)
	}
	if len(comss.IPs) < 2 || comss.IPs[0] != "1.2.3.4" {
		t.Errorf("первым должен быть живой IP: %+v", comss.IPs)
	}
	if comss.RTTMs == nil || *comss.RTTMs != 12 {
		t.Errorf("rttMs = %v", comss.RTTMs)
	}
	if len(comss.Checks) != 1 || len(comss.Checks[0].DeadIPs) != 1 || comss.Checks[0].DeadIPs[0] != "9.9.9.9" {
		t.Errorf("deadIps: %+v", comss.Checks)
	}

	before := resolveCalls.Load()
	again := doRequest(h, http.MethodGet, "/api/dns", "", nil)
	if again.Code != http.StatusOK {
		t.Fatalf("второй GET /api/dns = %d", again.Code)
	}
	if resolveCalls.Load() != before {
		t.Errorf("кэш не сработал: resolveCalls %d -> %d", before, resolveCalls.Load())
	}

	post := doRequest(h, http.MethodPost, "/api/dns", "", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/dns = %d", post.Code)
	}
	evil := doRequest(h, http.MethodGet, "/api/dns", "", map[string]string{"Origin": "https://evil.example"})
	if evil.Code != http.StatusForbidden {
		t.Errorf("чужой origin для DNS = %d, want 403", evil.Code)
	}
}

func TestDNSRateLimitWithoutCache(t *testing.T) {
	cfg := testConfig()
	cfg.DNSCache = -1
	cfg.DNSLimit = 1
	h := newTestHandler(t, cfg)

	if rr := doRequest(h, http.MethodGet, "/api/dns", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("первый = %d", rr.Code)
	}
	limited := doRequest(h, http.MethodGet, "/api/dns", "", nil)
	if limited.Code != http.StatusTooManyRequests {
		t.Errorf("лимит DNS = %d, want 429", limited.Code)
	}
	if got := decodeJSON[errorResponse](t, limited); got.Code != "RATE_LIMITED" {
		t.Errorf("код = %q", got.Code)
	}
}

func TestProxyOriginHSTSAndClientIP(t *testing.T) {
	cfg := testConfig()
	cfg.TrustProxy = true
	cfg.HSTS = true
	cfg.RegisterLimit = 1
	cfg.AllowedOrigins = []string{"https://app.example"}
	h := newTestHandler(t, cfg)

	first := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type":      "application/json",
		"Origin":            "https://example.com",
		"X-Forwarded-Proto": "https",
		"CF-Connecting-IP":  "198.51.100.1",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("same-origin за прокси = %d: %s", first.Code, first.Body.String())
	}
	if first.Header().Get("Strict-Transport-Security") == "" {
		t.Error("нет HSTS при --hsts")
	}

	index := doRequest(h, http.MethodGet, "/", "", nil)
	if csp := index.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "https://app.example") {
		t.Errorf("CSP не содержит allowlist: %s", csp)
	}

	// Другой клиентский IP (из CF-Connecting-IP) не должен делить лимит.
	second := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type":      "application/json",
		"CF-Connecting-IP":  "198.51.100.2",
		"X-Forwarded-Proto": "https",
	})
	if second.Code != http.StatusOK {
		t.Errorf("второй IP = %d", second.Code)
	}
	third := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type":     "application/json",
		"CF-Connecting-IP": "198.51.100.1",
	})
	if third.Code != http.StatusTooManyRequests {
		t.Errorf("третий запрос с того же IP = %d, want 429", third.Code)
	}

	// Без --trust-proxy заголовки не читаются: https-origin при http-запросе
	// считается чужим.
	noProxyCfg := testConfig()
	noProxyCfg.TrustProxy = false
	hNoProxy := newTestHandler(t, noProxyCfg)
	untrusted := doRequest(hNoProxy, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type":      "application/json",
		"Origin":            "https://example.com",
		"X-Forwarded-Proto": "https",
	})
	if untrusted.Code != http.StatusForbidden {
		t.Errorf("без trust-proxy = %d, want 403", untrusted.Code)
	}
}

func TestForwardedHost(t *testing.T) {
	cfg := testConfig()
	cfg.TrustProxy = true
	h := newTestHandler(t, cfg)
	rr := doRequest(h, http.MethodPost, "/api/warp", "{}", map[string]string{
		"Content-Type":      "application/json",
		"Origin":            "https://public.example",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Host":  "public.example",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("X-Forwarded-Host = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHealth(t *testing.T) {
	h := newTestHandler(t, testConfig())
	rr := doRequest(h, http.MethodGet, "/api/health", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("health = %d", rr.Code)
	}
	body := decodeJSON[map[string]any](t, rr)
	if body["ok"] != true || body["service"] != "warpgen-web" {
		t.Errorf("health = %+v", body)
	}
	post := doRequest(h, http.MethodPost, "/api/health", "", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST health = %d", post.Code)
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := testConfig()
	cfg.TLSCert = "cert.pem"
	if _, err := New(cfg); err == nil {
		t.Error("TLS без ключа должен падать")
	}

	cfg = testConfig()
	cfg.AllowedOrigins = []string{"https://example.org/path"}
	if _, err := New(cfg); err == nil {
		t.Error("origin с путём должен падать")
	}

	cfg = testConfig()
	cfg.AllowedOrigins = []string{"ftp://example.org"}
	if _, err := New(cfg); err == nil {
		t.Error("не http(s) origin должен падать")
	}

	cfg = testConfig()
	cfg.AllowedOrigins = []string{"https://user@example.org"}
	if _, err := New(cfg); err == nil {
		t.Error("origin с userinfo должен падать")
	}
}

func TestStripMask(t *testing.T) {
	if got := stripMask("172.16.0.2/32"); got != "172.16.0.2" {
		t.Errorf("stripMask = %q", got)
	}
	if got := stripMask("172.16.0.2"); got != "172.16.0.2" {
		t.Errorf("stripMask без маски = %q", got)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		host, scheme, want string
	}{
		{"example.com:443", "https", "example.com"},
		{"example.com:80", "http", "example.com"},
		{"example.com:8443", "https", "example.com:8443"},
		{"[::1]:443", "https", "::1"},
		{"[::1]", "https", "::1"},
		{"[::1]:8443", "https", "[::1]:8443"},
		{"example.com", "https", "example.com"},
	}
	for _, tc := range cases {
		if got := normalizeHost(tc.host, tc.scheme); got != tc.want {
			t.Errorf("normalizeHost(%q, %q) = %q, want %q", tc.host, tc.scheme, got, tc.want)
		}
	}
}

func findPreset(t *testing.T, presets []dnsPresetReport, id string) dnsPresetReport {
	t.Helper()
	for _, p := range presets {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("нет пресета %q", id)
	return dnsPresetReport{}
}
