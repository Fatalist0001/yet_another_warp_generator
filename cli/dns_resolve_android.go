//go:build android

package cli

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
)

// На Android Go-резолвер (netgo, CGO выключен) может не найти
// /etc/resolv.conf: DNS там раздаёт netd. Сначала пробуем системный резолв,
// затем — прямой UDP к публичным резолверам. Приложение исключено из VPN
// (addDisallowedApplication), поэтому этот запрос идёт напрямую, не в TUN.
//
// Это нужно только для --tun: WireGuard принимает в DNS исключительно IP,
// а GUI по умолчанию выбирает пресет с hostname'ами (Comss/Malw/GeoHide).
func init() {
	resolveDNSHost = resolveDNSHostAndroid
}

func resolveDNSHostAndroid(host string) (string, error) {
	systemErr := error(nil)
	if ip, err := config.ResolveDNS(host); err == nil {
		return ip, nil
	} else {
		systemErr = err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var lastErr error
	for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53"} {
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
