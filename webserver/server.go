// Package webserver — боевой HTTP-сервер веб-генератора.
//
// Он отдаёт те же статические файлы, что и `web/dev-server.js`, и тот же
// контракт `/api/warp` + `/api/dns`, но использует Go-пакеты репозитория
// (`registration`, `config`, `dnscheck`) и закрывает то, чего у эталонного
// дев-сервера быть не может: HTTPS, CORS по allowlist, POST-only регистрацию,
// rate limit, ограничение параллельных регистраций, security-заголовки и
// graceful shutdown.
//
// Сервер не хранит аккаунты и не пишет ключи в журнал: приватный ключ живёт
// только в ответе на запрос.
package webserver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	webassets "github.com/Fatalist0001/yet_another_warp_generator/web"
)

const (
	defaultRegisterLimit       = 10
	defaultRegisterHourLimit   = 60
	defaultRegisterGlobalLimit = 60
	defaultDNSLimit            = 30
	defaultMaxInflight         = 16
	defaultDNSCache            = 60 * time.Second
	defaultDNSTimeout          = 15 * time.Second
	registerTimeout            = 20 * time.Second
	maxRequestBytes            = 1024
)

// Config — настройки сервера. Нулевые значения заменяются разумными
// значениями по умолчанию; отрицательные лимиты отключают соответствующий
// ограничитель (нужно для тестов и приватных запусков).
type Config struct {
	// Listen — адрес прослушивания, по умолчанию 127.0.0.1:8080.
	Listen string
	// TLSCert/TLSKey — PEM-файлы. Задаются вместе; иначе сервер работает по HTTP
	// (например, за reverse proxy, который терминирует TLS).
	TLSCert string
	TLSKey  string
	// AllowedOrigins — точные origin'ы для CORS и проверки POST. По умолчанию
	// разрешён только собственный origin страницы.
	AllowedOrigins []string
	// TrustProxy — доверять CF-Connecting-IP / X-Forwarded-For и
	// X-Forwarded-Proto. Включать только когда перед сервером стоит свой прокси,
	// который сам выставляет эти заголовки.
	TrustProxy bool
	// HSTS — отдавать Strict-Transport-Security. Нужен, когда TLS терминирует
	// прокси, а сам сервер слушает HTTP.
	HSTS       bool
	HSTSMaxAge time.Duration

	RegisterLimit       int // регистраций в минуту на IP
	RegisterHourLimit   int // регистраций в час на IP
	RegisterGlobalLimit int // регистраций в минуту всего
	DNSLimit            int // проверок DNS в минуту на IP
	MaxInflight         int // одновременных регистраций
	DNSCache            time.Duration
	DNSTimeout          time.Duration

	Logger  *log.Logger
	Version string

	// Function-хуки для офлайн-тестов. В обычном запуске nil — используются
	// реальные registration.Register, net.DefaultResolver и dnscheck.
	RegisterFunc func(context.Context) (registration.Account, error)
	Resolve4Func func(context.Context, string) ([]string, error)
	ProbeDNSFunc func(context.Context, string) ProbeResult
	NowFunc      func() time.Time
}

func (c *Config) setDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.RegisterLimit == 0 {
		c.RegisterLimit = defaultRegisterLimit
	}
	if c.RegisterHourLimit == 0 {
		c.RegisterHourLimit = defaultRegisterHourLimit
	}
	if c.RegisterGlobalLimit == 0 {
		c.RegisterGlobalLimit = defaultRegisterGlobalLimit
	}
	if c.DNSLimit == 0 {
		c.DNSLimit = defaultDNSLimit
	}
	if c.MaxInflight <= 0 {
		c.MaxInflight = defaultMaxInflight
	}
	if c.DNSCache == 0 {
		c.DNSCache = defaultDNSCache
	}
	if c.DNSCache < 0 {
		c.DNSCache = 0
	}
	if c.DNSTimeout <= 0 {
		c.DNSTimeout = defaultDNSTimeout
	}
	if c.HSTSMaxAge <= 0 {
		c.HSTSMaxAge = 365 * 24 * time.Hour
	}
	if c.Logger == nil {
		c.Logger = log.New(os.Stderr, "warpgen web: ", log.LstdFlags)
	}
	if c.NowFunc == nil {
		c.NowFunc = time.Now
	}
	if c.Resolve4Func == nil {
		c.Resolve4Func = defaultResolve4
	}
	if c.ProbeDNSFunc == nil {
		c.ProbeDNSFunc = defaultProbeDNS
	}
	if c.RegisterFunc == nil {
		client := registration.NewClient()
		c.RegisterFunc = func(ctx context.Context) (registration.Account, error) {
			return registration.Register(ctx, client)
		}
	}
}

// Server — HTTP-обработчик и его состояние (лимиты, кэш DNS).
type Server struct {
	cfg            Config
	assets         map[string]staticAsset
	csp            string
	allowedOrigins map[string]bool

	registerIP     *rateLimiter
	registerHour   *rateLimiter
	registerGlobal *rateLimiter
	dnsRate        *rateLimiter
	registerSem    chan struct{}

	dnsMu      sync.Mutex
	dnsAt      time.Time
	dnsPresets []dnsPresetReport
}

type staticAsset struct {
	data        []byte
	contentType string
	cache       string
	etag        string
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type warpResponse struct {
	PrivKey    string `json:"privKey"`
	PeerPub    string `json:"peer_pub"`
	ClientIPv4 string `json:"client_ipv4,omitempty"`
	ClientIPv6 string `json:"client_ipv6,omitempty"`
}

// New собирает сервер, валидирует конфигурацию и читает встроенные ассеты.
func New(cfg Config) (*Server, error) {
	cfg.setDefaults()
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, errors.New("TLS: нужны оба флага — --tls-cert и --tls-key")
	}

	s := &Server{
		cfg:            cfg,
		assets:         make(map[string]staticAsset),
		allowedOrigins: make(map[string]bool),
		registerIP:     newRateLimiter(cfg.RegisterLimit, time.Minute, cfg.NowFunc),
		registerHour:   newRateLimiter(cfg.RegisterHourLimit, time.Hour, cfg.NowFunc),
		registerGlobal: newRateLimiter(cfg.RegisterGlobalLimit, time.Minute, cfg.NowFunc),
		dnsRate:        newRateLimiter(cfg.DNSLimit, time.Minute, cfg.NowFunc),
		registerSem:    make(chan struct{}, cfg.MaxInflight),
	}
	for _, origin := range cfg.AllowedOrigins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			(u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("некорректный --allowed-origins: %q", origin)
		}
		s.allowedOrigins[strings.ToLower(origin)] = true
	}

	if err := s.loadAssets(); err != nil {
		return nil, err
	}
	s.csp = s.buildCSP()
	return s, nil
}

// Handler — корневой обработчик (используется и в тестах через httptest).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/warp", s.handleWarp)
	mux.HandleFunc("/api/dns", s.handleDNS)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/", s.handleStatic)
	return s.recoverer(s.logger(s.security(s.cors(mux))))
}

func (s *Server) loadAssets() error {
	add := func(urlPath, file, contentType, cache string) error {
		data, err := fs.ReadFile(webassets.Assets, file)
		if err != nil {
			return fmt.Errorf("встроенный файл %s: %w", file, err)
		}
		sum := sha256.Sum256(data)
		s.assets[urlPath] = staticAsset{
			data:        data,
			contentType: contentType,
			cache:       cache,
			etag:        `"` + hex.EncodeToString(sum[:12]) + `"`,
		}
		return nil
	}

	if err := add("/", "index.html", "text/html; charset=utf-8", "no-cache"); err != nil {
		return err
	}
	if err := add("/index.html", "index.html", "text/html; charset=utf-8", "no-cache"); err != nil {
		return err
	}
	if err := add("/app.js", "app.js", "text/javascript; charset=utf-8", "no-cache"); err != nil {
		return err
	}
	if err := add("/warpgen.js", "warpgen.js", "text/javascript; charset=utf-8", "no-cache"); err != nil {
		return err
	}

	entries, err := fs.ReadDir(webassets.Assets, "fonts")
	if err != nil {
		return fmt.Errorf("встроенные шрифты: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".woff2"):
			err = add("/fonts/"+name, "fonts/"+name, "font/woff2", "public, max-age=86400")
		case strings.HasSuffix(name, ".txt"):
			err = add("/fonts/"+name, "fonts/"+name, "text/plain; charset=utf-8", "public, max-age=86400")
		default:
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) buildCSP() string {
	connect := []string{"'self'", "https://cloudflare-dns.com"}
	for origin := range s.allowedOrigins {
		connect = append(connect, origin)
	}
	parts := []string{
		"default-src 'self'",
		"base-uri 'none'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src " + strings.Join(connect, " "),
	}
	if s.cfg.TLSCert != "" || s.cfg.HSTS {
		parts = append(parts, "upgrade-insecure-requests")
	}
	return strings.Join(parts, "; ")
}

func (s *Server) tlsEnabled() bool {
	return s.cfg.TLSCert != "" && s.cfg.TLSKey != ""
}

// ---------- middleware ----------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.cfg.Logger.Printf("паника при обработке %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
				if rec.status == 0 {
					writeError(rec, http.StatusInternalServerError, "внутренняя ошибка сервера", "INTERNAL")
				}
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

func (s *Server) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.cfg.Logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy", s.csp)
		// По умолчанию ответы не кэшируются; статика ниже переопределяет это
		// для шрифтов, у которых есть ETag и стабильное имя.
		h.Set("Cache-Control", "no-store")
		if s.tlsEnabled() || s.cfg.HSTS {
			h.Set("Strict-Transport-Security", "max-age="+strconv.Itoa(int(s.cfg.HSTSMaxAge.Seconds())))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				if !s.originAllowed(r) {
					writeError(w, http.StatusForbidden, "доступ с этого origin запрещён", "ORIGIN_NOT_ALLOWED")
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true // curl/серверные клиенты: браузерной CSRF здесь нет
	}
	if origin == "null" {
		return false
	}
	if s.allowedOrigins[strings.ToLower(origin)] {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return false
	}
	scheme := s.requestScheme(r)
	if !strings.EqualFold(u.Scheme, scheme) {
		return false
	}
	return strings.EqualFold(normalizeHost(u.Host, scheme), normalizeHost(s.requestHost(r), scheme))
}

func (s *Server) requestHost(r *http.Request) string {
	if s.cfg.TrustProxy {
		if host := r.Header.Get("X-Forwarded-Host"); host != "" {
			if i := strings.IndexByte(host, ','); i >= 0 {
				host = host[:i]
			}
			host = strings.TrimSpace(host)
			if host != "" {
				return host
			}
		}
	}
	return r.Host
}

func normalizeHost(host, scheme string) string {
	if h, port, err := net.SplitHostPort(host); err == nil {
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			return h
		}
		return net.JoinHostPort(h, port)
	}
	// Origin для IPv6 без порта приходит в скобках, а r.Host — тоже; сравниваем
	// без скобок, чтобы [::1] и [::1]:443 свести к одному виду.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

func (s *Server) requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if s.cfg.TrustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			if i := strings.IndexByte(proto, ','); i >= 0 {
				proto = proto[:i]
			}
			proto = strings.ToLower(strings.TrimSpace(proto))
			if proto == "http" || proto == "https" {
				return proto
			}
		}
	}
	return "http"
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); v != "" {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			parts := strings.Split(v, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				part := strings.TrimSpace(parts[i])
				if ip := net.ParseIP(part); ip != nil {
					return ip.String()
				}
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

// ---------- API ----------

func (s *Server) handleWarp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "только POST: тело {} и Content-Type: application/json", "METHOD_NOT_ALLOWED")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "нужен Content-Type: application/json", "UNSUPPORTED_MEDIA_TYPE")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "тело запроса слишком большое", "REQUEST_TOO_LARGE")
			return
		}
		writeError(w, http.StatusBadRequest, "не удалось прочитать тело запроса", "BAD_REQUEST")
		return
	}

	ip := s.clientIP(r)
	retryAfter := 0
	switch {
	case !s.registerIP.allow(ip):
		retryAfter = 60
	case !s.registerHour.allow(ip):
		retryAfter = 3600
	case !s.registerGlobal.allow("global"):
		retryAfter = 60
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, "слишком много регистраций, подождите", "RATE_LIMITED")
		return
	}

	select {
	case s.registerSem <- struct{}{}:
		defer func() { <-s.registerSem }()
	default:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "сервер занят регистрациями, попробуйте через несколько секунд", "SERVER_BUSY")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), registerTimeout)
	defer cancel()
	acc, err := s.cfg.RegisterFunc(ctx)
	if err != nil {
		s.cfg.Logger.Printf("регистрация WARP: %v", err)
		status, code, message := classifyRegisterError(err)
		writeError(w, status, message, code)
		return
	}
	v4, v6 := stripMask(acc.IPv4), stripMask(acc.IPv6)
	if !acc.Complete() || (net.ParseIP(v4) == nil && net.ParseIP(v6) == nil) {
		s.cfg.Logger.Printf("регистрация WARP: неполный аккаунт (ключи=%v, адреса=%q/%q)", acc.Complete(), acc.IPv4, acc.IPv6)
		writeError(w, http.StatusBadGateway, "Cloudflare вернул неполный аккаунт", "REGISTRATION_FAILED")
		return
	}

	writeJSON(w, http.StatusOK, warpResponse{
		PrivKey:    acc.PrivateKey,
		PeerPub:    acc.PeerPublicKey,
		ClientIPv4: v4,
		ClientIPv6: v6,
	})
}

func (s *Server) handleDNS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "только GET", "METHOD_NOT_ALLOWED")
		return
	}

	if presets, ok := s.cachedDNS(); ok {
		writeJSON(w, http.StatusOK, dnsResponse{Presets: presets})
		return
	}

	if !s.dnsRate.allow(s.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "слишком много проверок DNS, подождите минуту", "RATE_LIMITED")
		return
	}

	// Кэш общий: строим отчёт под блокировкой, чтобы параллельные запросы не
	// устраивали лавину UDP-проверок. Контекст не привязан к клиенту: если он
	// отключился, отчёт всё равно догрузится для следующих.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DNSTimeout)
	defer cancel()
	presets := s.refreshDNS(ctx)
	writeJSON(w, http.StatusOK, dnsResponse{Presets: presets})
}

func (s *Server) cachedDNS() ([]dnsPresetReport, bool) {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	if s.cfg.DNSCache > 0 && s.dnsPresets != nil && s.cfg.NowFunc().Sub(s.dnsAt) < s.cfg.DNSCache {
		return s.dnsPresets, true
	}
	return nil, false
}

func (s *Server) refreshDNS(ctx context.Context) []dnsPresetReport {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	if s.cfg.DNSCache > 0 && s.dnsPresets != nil && s.cfg.NowFunc().Sub(s.dnsAt) < s.cfg.DNSCache {
		return s.dnsPresets
	}
	presets := buildDNSReport(ctx, s.cfg.Resolve4Func, s.cfg.ProbeDNSFunc)
	// Отчёт, собранный на грани таймаута, не кэшируем: иначе одна сетевая
	// просадка пометит все резолверы мёртвыми на весь TTL.
	if s.cfg.DNSCache <= 0 || len(presets) == 0 || ctx.Err() != nil {
		return presets
	}
	s.dnsPresets = presets
	s.dnsAt = s.cfg.NowFunc()
	return presets
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "только GET", "METHOD_NOT_ALLOWED")
		return
	}
	body := map[string]any{"ok": true, "service": "warpgen-web"}
	if s.cfg.Version != "" {
		body["version"] = s.cfg.Version
	}
	writeJSON(w, http.StatusOK, body)
}

// ---------- статика ----------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "только GET", "METHOD_NOT_ALLOWED")
		return
	}
	asset, ok := s.assets[r.URL.Path]
	if !ok {
		writeError(w, http.StatusNotFound, "не найдено", "NOT_FOUND")
		return
	}
	w.Header().Set("Cache-Control", asset.cache)
	w.Header().Set("ETag", asset.etag)
	if etagMatches(r.Header.Get("If-None-Match"), asset.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.data)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(asset.data)
}

func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// ---------- запуск ----------

// Run создаёт сервер и работает до отмены ctx (SIGINT/SIGTERM в CLI).
func Run(ctx context.Context, cfg Config) error {
	s, err := New(cfg)
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}

// Serve — цикл прослушивания с graceful shutdown по ctx.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ErrorLog:          s.cfg.Logger,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		if s.tlsEnabled() {
			errCh <- srv.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
			return
		}
		errCh <- srv.ListenAndServe()
	}()

	scheme := "http"
	if s.tlsEnabled() {
		scheme = "https"
	}
	s.cfg.Logger.Printf("слушаю %s://%s", scheme, s.cfg.Listen)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Не валим процесс из-за медленного запроса: соединения всё равно
			// закроются при выходе, а состояние на диск сервер не пишет.
			s.cfg.Logger.Printf("остановка: %v", err)
		}
		s.cfg.Logger.Print("остановлен")
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// ---------- утилиты ----------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, errorResponse{Error: message, Code: code})
}

func stripMask(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

func classifyRegisterError(err error) (status int, code, message string) {
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"):
		return http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "Cloudflare не ответил вовремя"
	case strings.Contains(text, "статус 429"), strings.Contains(text, "error code: 1015"), strings.Contains(text, "too many requests"):
		return http.StatusTooManyRequests, "RATE_LIMITED", "Cloudflare ограничил регистрацию, попробуйте позже"
	}

	var netErr net.Error
	if errors.As(err, &netErr) || strings.Contains(text, "no such host") ||
		strings.Contains(text, "connection refused") || strings.Contains(text, "network is unreachable") ||
		strings.Contains(text, "connect:") || strings.Contains(text, "dial ") || strings.Contains(text, "eof") {
		return http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "Cloudflare недоступен с сервера"
	}
	return http.StatusBadGateway, "REGISTRATION_FAILED", "не удалось зарегистрировать аккаунт"
}
