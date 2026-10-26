package vpn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// resolverFarm — поддельный SOCKS5, который отдаёт CONNECT к адресам резолверов
// локальным DNS-серверам. Никакого выхода в сеть.
type resolverFarm struct {
	socks *fakeSOCKS5

	mu      sync.Mutex
	routes  map[string]string
	refused map[string]bool
	asked   []string
}

func newResolverFarm(t *testing.T) *resolverFarm {
	t.Helper()
	f := &resolverFarm{routes: map[string]string{}, refused: map[string]bool{}}
	f.socks = newFakeSOCKS5(t, func(target string) (net.Conn, error) {
		f.mu.Lock()
		f.asked = append(f.asked, target)
		addr, ok := f.routes[target]
		refuse := f.refused[target]
		f.mu.Unlock()
		if refuse || !ok {
			return nil, fmt.Errorf("резолвер %s недоступен", target)
		}
		return net.Dial("tcp", addr)
	})
	return f
}

func (f *resolverFarm) route(target, localDNS string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[target] = localDNS
}

func (f *resolverFarm) refuse(target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused[target] = true
}

func (f *resolverFarm) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *resolverFarm) resolver(t *testing.T, addrs ...string) *Resolver {
	t.Helper()
	dial := DialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialSocks5(ctx, f.socks.addr(), address, nil)
	})
	r, err := NewResolver(addrs, dial, time.Second, t.Logf)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return r
}

// Резолвер выбирается по замеру задержки.
func TestResolverSelectsFastest(t *testing.T) {
	farm := newResolverFarm(t)
	slow, _ := dnsOverTCP(t, 40*time.Millisecond)
	fast, _ := dnsOverTCP(t, 0)
	farm.route("1.1.1.1:53", slow)
	farm.route("8.8.8.8:53", fast)

	r := farm.resolver(t, "1.1.1.1:53", "8.8.8.8:53")
	addr, rtt, err := r.Select(context.Background())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if addr != "8.8.8.8:53" {
		t.Errorf("выбран %q, ожидали 8.8.8.8:53", addr)
	}
	if rtt <= 0 {
		t.Errorf("замер задержки = %v", rtt)
	}
	if r.Effective() != "8.8.8.8:53" {
		t.Errorf("Effective = %q", r.Effective())
	}
	if asked := farm.requested(); len(asked) != 2 {
		t.Errorf("проверено %v, ожидали оба кандидата", asked)
	}
}

// При смене звена цепочки резолвер выбирается заново.
func TestResolverReselectsAfterFailure(t *testing.T) {
	farm := newResolverFarm(t)
	first, _ := dnsOverTCP(t, 0)
	second, _ := dnsOverTCP(t, 30*time.Millisecond)
	farm.route("1.1.1.1:53", first)
	farm.route("8.8.8.8:53", second)

	r := farm.resolver(t, "1.1.1.1:53", "8.8.8.8:53")
	if addr, _, err := r.Select(context.Background()); err != nil || addr != "1.1.1.1:53" {
		t.Fatalf("первый выбор: %q, %v", addr, err)
	}

	farm.refuse("1.1.1.1:53")
	addr, _, err := r.Select(context.Background())
	if err != nil {
		t.Fatalf("перевыбор: %v", err)
	}
	if addr != "8.8.8.8:53" {
		t.Errorf("после отказа выбран %q, ожидали 8.8.8.8:53", addr)
	}
}

// До первого выбора мост пользуется первым кандидатом, а не пустотой.
func TestResolverEffectiveBeforeSelect(t *testing.T) {
	farm := newResolverFarm(t)
	r := farm.resolver(t, "1.1.1.1", "8.8.8.8:5353")
	if got := r.Effective(); got != "1.1.1.1:53" {
		t.Errorf("Effective = %q, ожидали 1.1.1.1:53", got)
	}
}

// Ни один резолвер не ответил — ошибка, но выбор остаётся прежним.
func TestResolverAllFail(t *testing.T) {
	farm := newResolverFarm(t)
	while, _ := dnsOverTCP(t, 0)
	farm.route("1.1.1.1:53", while)

	r := farm.resolver(t, "1.1.1.1:53", "8.8.8.8:53")
	if addr, _, err := r.Select(context.Background()); err != nil || addr != "1.1.1.1:53" {
		t.Fatalf("первый выбор: %q, %v", addr, err)
	}

	farm.refuse("1.1.1.1:53")
	addr, _, err := r.Select(context.Background())
	if err == nil {
		t.Fatalf("ожидали ошибку, получили %q", addr)
	}
	if !strings.Contains(err.Error(), "ни один DNS") {
		t.Errorf("текст ошибки = %q", err)
	}
	if got := r.Effective(); got != "1.1.1.1:53" {
		t.Errorf("Effective = %q: прошлый выбор должен сохраниться", got)
	}
}

func TestParseDNSTarget(t *testing.T) {
	cases := []struct {
		in      string
		host    string
		port    int
		wantErr bool
	}{
		{in: "1.1.1.1", host: "1.1.1.1", port: 0},
		{in: "1.1.1.1:5353", host: "1.1.1.1", port: 5353},
		{in: "[2606:4700:4700::1111]:53", host: "2606:4700:4700::1111", port: 53},
		{in: "2606:4700:4700::1111", host: "2606:4700:4700::1111", port: 0},
		{in: "dns.example", wantErr: true},
		{in: "1.1.1.1:0", wantErr: true},
		{in: "1.1.1.1:70000", wantErr: true},
		{in: "1.1.1.1:abc", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseDNSTarget(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: ожидали ошибку, получили %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.IP != c.host || got.Port != c.port {
			t.Errorf("%q: получили %s (порт %d), ожидали %s (порт %d)", c.in, got.IP, got.Port, c.host, c.port)
		}
	}
}

func TestParseUDPPolicy(t *testing.T) {
	for _, ok := range []string{"", "drop", "DROP", " drop "} {
		if _, err := ParseUDPPolicy(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := ParseUDPPolicy("proxy"); err == nil {
		t.Error("неизвестная политика должна быть отвергнута")
	}
}
