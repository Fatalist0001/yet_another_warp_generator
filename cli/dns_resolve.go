package cli

import (
	"fmt"
	"net"
	"strings"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
)

// resolveDNSHost — точка подмены в тестах: резолв hostname'а в IP.
var resolveDNSHost = config.ResolveDNS

// resolveDNSAddrs переводит DNS-адреса в IP. Системный туннель принимает только
// IP: мост отправляет DNS-запросы по TCP через SOCKS5, а не резолвит имена.
// Адреса с портом сохраняют порт.
func resolveDNSAddrs(addrs []string) ([]string, error) {
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		host, port := raw, ""
		if h, p, err := net.SplitHostPort(raw); err == nil {
			host, port = h, p
		}
		if ip := net.ParseIP(host); ip != nil {
			out = append(out, raw)
			continue
		}
		ip, err := resolveDNSHost(host)
		if err != nil {
			return nil, fmt.Errorf("DNS %q: не удалось определить IP (%v); укажите IP-адрес", raw, err)
		}
		if port != "" {
			out = append(out, net.JoinHostPort(ip, port))
		} else {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("пустой список DNS")
	}
	return out, nil
}
