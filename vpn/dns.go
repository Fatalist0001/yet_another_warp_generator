package vpn

import (
	"context"
	"encoding/binary"
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

	addr := targetAddr(best)
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
	return targetAddr(r.targets[0])
}

// Candidates — резолверы для одной попытки: выбранный первым, за ним остальные
// в порядке предпочтения.
//
// Мост пробует их по очереди. Один адрес, зафиксированный на весь прогон, —
// ловушка: резолвер, который не отвечает (сменилось звено цепочки, адрес
// заблокирован у провайдера), оставлял бы систему без DNS до перезапуска, и
// выглядело бы это как «интернета нет» после подключения.
func (r *Resolver) Candidates() []string {
	r.mu.RLock()
	cur := r.current
	r.mu.RUnlock()

	out := make([]string, 0, len(r.targets)+1)
	seen := make(map[string]bool, len(r.targets)+1)
	add := func(addr string) {
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, addr)
	}
	add(cur)
	for _, t := range r.targets {
		add(targetAddr(t))
	}
	return out
}

// targetAddr — адрес резолвера в виде host:port (порт по умолчанию — 53).
func targetAddr(t dnscheck.Target) string {
	port := t.Port
	if port == 0 {
		port = dnscheck.DefaultPort
	}
	return net.JoinHostPort(t.IP, strconv.Itoa(port))
}

// dnsServfail собирает ответ SERVFAIL (RCODE=2) на запрос клиента: тот же id и
// тот же вопрос, без ответов и дополнительных записей.
//
// Нужен, чтобы не молчать, когда ни один резолвер не ответил. Молчание дороже
// ошибки: клиент ждёт свой таймаут на каждую попытку, а приложение в это время
// считает, что сети нет. На SERVFAIL клиент отказывается от попытки сразу.
func dnsServfail(query []byte) []byte {
	const headerLen = 12
	if len(query) < headerLen {
		return nil
	}
	questions := binary.BigEndian.Uint16(query[4:6])
	end := headerLen
	if questions > 0 {
		qend, err := questionEnd(query, headerLen)
		if err != nil {
			return nil
		}
		end = qend
	}

	resp := make([]byte, end)
	copy(resp, query[:end])

	flags := binary.BigEndian.Uint16(query[2:4])
	flags &^= 0x000f // RCODE
	flags &^= 0x0400 // AA
	flags &^= 0x0200 // TC
	flags &^= 0x0080 // RA
	flags |= 0x8000  // QR: это ответ
	flags |= 0x0002  // SERVFAIL
	binary.BigEndian.PutUint16(resp[2:4], flags)

	count := uint16(0)
	if questions > 0 {
		count = 1
	}
	binary.BigEndian.PutUint16(resp[4:6], count) // QDCOUNT
	binary.BigEndian.PutUint16(resp[6:8], 0)     // ANCOUNT
	binary.BigEndian.PutUint16(resp[8:10], 0)    // NSCOUNT
	binary.BigEndian.PutUint16(resp[10:12], 0)   // ARCOUNT
	return resp
}

// questionEnd — конец первого вопроса в DNS-сообщении (после QTYPE и QCLASS).
func questionEnd(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errors.New("вопрос DNS обрезан")
		}
		label := int(msg[off])
		off++
		if label == 0 {
			break
		}
		if label&0xc0 != 0 {
			return 0, errors.New("сжатие в вопросе DNS")
		}
		if off+label > len(msg) {
			return 0, errors.New("метка в вопросе DNS обрезана")
		}
		off += label
	}
	if off+4 > len(msg) {
		return 0, errors.New("вопрос DNS без QTYPE/QCLASS")
	}
	return off + 4, nil
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
