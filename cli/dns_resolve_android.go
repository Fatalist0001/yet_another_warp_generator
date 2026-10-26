//go:build android

package cli

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
)

// На Android Go-резолвер (netgo, CGO выключен) читает /etc/resolv.conf, а там
// оказывается ::1 — системного DNS из процесса приложения нет. В итоге падает
// не только выбор DNS для TUN, но и первый запрос к API Cloudflare: новый
// пользователь видит «API Cloudflare недоступен из этой сети».
//
// Поэтому подменяем net.DefaultResolver на публичные DNS без подмены.
// 77.88.8.8 сюда не входит: в РФ он отдаёт для api.cloudflareclient.com
// адрес 8.47.69.0, и регистрация всё равно ломается.
//
// Приложение исключено из VPN (addDisallowedApplication), поэтому запросы идут
// напрямую, не в TUN.
var (
	androidDNSMu     sync.Mutex
	androidDNSServer string
)

var androidDNSCandidates = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
	"9.9.9.9:53",
}

func init() {
	resolveDNSHost = resolveDNSHostAndroid
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial:     androidDNSDial,
	}
}

func androidDNSDial(ctx context.Context, network, _ string) (net.Conn, error) {
	server := androidDNSServerFor(ctx)
	dialer := net.Dialer{Timeout: 3 * time.Second}
	return dialer.DialContext(ctx, network, server)
}

// androidDNSServerFor выбирает первый ответивший публичный DNS. Пробуем
// параллельно, чтобы не тратить на недоступный сервер весь таймаут запроса.
// Неудачу не кэшируем: следующий запрос попробует снова.
func androidDNSServerFor(ctx context.Context) string {
	androidDNSMu.Lock()
	defer androidDNSMu.Unlock()
	if androidDNSServer != "" {
		return androidDNSServer
	}

	// 700 мс достаточно для ответа публичного DNS; остаток таймаута
	// регистрации (3 с) остаётся на сам HTTP-запрос.
	probeCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()

	type result struct {
		server string
		ok     bool
	}
	ch := make(chan result, len(androidDNSCandidates))
	for _, server := range androidDNSCandidates {
		go func(server string) {
			resolver := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					dialer := net.Dialer{Timeout: 700 * time.Millisecond}
					return dialer.DialContext(ctx, network, server)
				},
			}
			_, err := resolver.LookupHost(probeCtx, "example.com")
			ch <- result{server: server, ok: err == nil}
		}(server)
	}
	for range androidDNSCandidates {
		res := <-ch
		if res.ok {
			androidDNSServer = res.server
			return res.server
		}
	}
	return androidDNSCandidates[0]
}

func resolveDNSHostAndroid(host string) (string, error) {
	systemErr := error(nil)
	if ip, err := config.ResolveDNS(host); err == nil {
		return ip, nil
	} else {
		systemErr = err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var lastErr error
	for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53", "77.88.8.8:53", "9.9.9.9:53"} {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				dialer := net.Dialer{Timeout: 3 * time.Second}
				return dialer.DialContext(ctx, "udp", server)
			},
		}
		ips, err := resolver.LookupHost(ctx, host)
		if err != nil {
			lastErr = err
			continue
		}
		for _, ip := range ips {
			if v4 := net.ParseIP(ip).To4(); v4 != nil {
				return v4.String(), nil
			}
		}
		if len(ips) > 0 {
			return ips[0], nil
		}
		lastErr = fmt.Errorf("нет адресов")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("нет адресов")
	}
	return "", fmt.Errorf("системный резолв: %v; публичные DNS: %w", systemErr, lastErr)
}
