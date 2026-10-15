package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/dnscheck"
)

// Resolver — резолверы-кандидаты и выбранный из них. Выбор делается через саму
// цепочку (DNS-over-TCP через SOCKS5) и повторяется при смене звена: без этого
// система продолжала бы спрашивать резолвер, до которого нет маршрута.
type Resolver struct {
	targets []dnscheck.Target
	dial    DialFunc
	opts    dnscheck.Options
	logf    func(format string, args ...any)

	mu      sync.RWMutex
	current string
}

// NewResolver разбирает адреса резолверов: "1.1.1.1", "1.1.1.1:53", "[::1]:53".
func NewResolver(addrs []string, dial DialFunc, timeout time.Duration, logf func(format string, args ...any)) (*Resolver, error) {
	if dial == nil {
		return nil, errors.New("vpn: не задан дозвон через SOCKS5")
	}
	targets := make([]dnscheck.Target, 0, len(addrs))
	for _, s := range addrs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		t, err := parseDNSTarget(s)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return nil, errors.New("vpn: не задан список DNS-резолверов")
	}
	return &Resolver{
		targets: targets,
		dial:    dial,
		opts:    dnscheck.Options{Timeout: timeout},
		logf:    logf,
	}, nil
}

// Select проверяет кандидатов через цепочку и запоминает лучший. Возвращает его
// адрес (ip:port) и замер задержки.
func (r *Resolver) Select(ctx context.Context) (string, time.Duration, error) {
	best, results, err := dnscheck.SelectBestTCP(ctx, r.dial, r.targets, r.opts)
	if r.logf != nil {
		for _, res := range results {
			if !res.Status.OK() {
				r.logf("DNS %s: %s (%v)", res.Name, res.Status, res.Err)
			}
		}
	}
	if err != nil {
		return "", 0, err
	}

	port := best.Port
	if port == 0 {
		port = dnscheck.DefaultPort
	}
	addr := net.JoinHostPort(best.IP, strconv.Itoa(port))
	var rtt time.Duration
	for _, res := range results {
		if res.Name == best.Name {
			rtt = res.RTT
			break
		}
	}

	r.mu.Lock()
	r.current = addr
	r.mu.Unlock()
	return addr, rtt, nil
}

// Effective — текущий резолвер, а до первого выбора — первый кандидат: мост
// должен отвечать на DNS сразу, не дожидаясь проверки.
func (r *Resolver) Effective() string {
	r.mu.RLock()
	cur := r.current
	r.mu.RUnlock()
	if cur != "" {
		return cur
	}
	first := r.targets[0]
	port := first.Port
	if port == 0 {
		port = dnscheck.DefaultPort
	}
	return net.JoinHostPort(first.IP, strconv.Itoa(port))
}

// parseDNSTarget разбирает "ip" или "ip:port".
func parseDNSTarget(s string) (dnscheck.Target, error) {
	host, port := s, 0
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, cerr := strconv.Atoi(p)
		if cerr != nil || n < 1 || n > 65535 {
			return dnscheck.Target{}, fmt.Errorf("vpn: некорректный порт резолвера %q", s)
		}
		host, port = h, n
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return dnscheck.Target{}, fmt.Errorf("vpn: %q не является адресом резолвера (ожидается ip или ip:port)", s)
	}
	return dnscheck.Target{Name: s, IP: host, Port: port}, nil
}
