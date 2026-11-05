package cli

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/webserver"
)

// runWeb — боевой HTTP-сервер веб-генератора.
func runWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "адрес прослушивания (например, :8080)")
	tlsCert := fs.String("tls-cert", "", "TLS-сертификат (PEM); вместе с --tls-key включает HTTPS")
	tlsKey := fs.String("tls-key", "", "TLS-ключ (PEM)")
	allowedOrigins := fs.String("allowed-origins", "", "разрешённые Origin через запятую; по умолчанию только свой")
	trustProxy := fs.Bool("trust-proxy", false, "доверять CF-Connecting-IP/X-Forwarded-For, X-Forwarded-Proto и X-Forwarded-Host (только за своим прокси)")
	hsts := fs.Bool("hsts", false, "отдавать Strict-Transport-Security (если TLS терминируется прокси)")
	registerLimit := fs.Int("register-limit", 10, "регистраций в минуту на IP")
	registerHourLimit := fs.Int("register-hour-limit", 60, "регистраций в час на IP")
	registerGlobalLimit := fs.Int("register-global-limit", 60, "регистраций в минуту всего")
	dnsLimit := fs.Int("dns-limit", 30, "проверок DNS в минуту на IP")
	maxInflight := fs.Int("max-inflight", 16, "одновременных регистраций")
	dnsCache := fs.Duration("dns-cache", 60*time.Second, "кэш отчёта DNS; отрицательное значение (например, -1s) выключает")
	dnsTimeout := fs.Duration("dns-timeout", 15*time.Second, "таймаут проверки всего отчёта DNS")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var origins []string
	if strings.TrimSpace(*allowedOrigins) != "" {
		origins = strings.Split(*allowedOrigins, ",")
	}

	cfg := webserver.Config{
		Listen:              *listen,
		TLSCert:             *tlsCert,
		TLSKey:              *tlsKey,
		AllowedOrigins:      origins,
		TrustProxy:          *trustProxy,
		HSTS:                *hsts,
		RegisterLimit:       *registerLimit,
		RegisterHourLimit:   *registerHourLimit,
		RegisterGlobalLimit: *registerGlobalLimit,
		DNSLimit:            *dnsLimit,
		MaxInflight:         *maxInflight,
		DNSCache:            *dnsCache,
		DNSTimeout:          *dnsTimeout,
		Logger:              log.New(os.Stderr, "warpgen web: ", log.LstdFlags),
		Version:             Version,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return webserver.Run(ctx, cfg)
}
