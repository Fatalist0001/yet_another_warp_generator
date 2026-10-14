// Package dnscheck — проверка DNS через туннель.
//
// Проверяем весь путь клиент ↔ Cloudflare ↔ DNS: DNS-запрос уходит через
// userspace-туннель на выбранном endpoint, поэтому результат отражает реальный
// маршрут, а не локальную сеть.
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
)

// QueryName — имя, которое запрашиваем для проверки доступности DNS.
const QueryName = "cloudflare.com."

// SlowThreshold — отклик выше этого порога считается медленным.
const SlowThreshold = 500 * time.Millisecond

// DefaultTimeout — таймаут одной проверки.
const DefaultTimeout = 3 * time.Second

// Status — итог проверки одного DNS.
type Status int

// Возможные статусы.
const (
	// StatusOK — DNS ответил вовремя.
	StatusOK Status = iota
	// StatusSlow — DNS ответил, но медленно.
	StatusSlow
	// StatusNoAnswer — нет ответа (таймаут / пустой ответ).
	StatusNoAnswer
	// StatusError — ошибка на уровне соединения или разбора.
	StatusError
)

// String — человекочитаемая метка.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "работает"
	case StatusSlow:
		return "медленный"
	case StatusNoAnswer:
		return "нет ответа"
	default:
		return "ошибка"
	}
}

// OK сообщает, что DNS пригоден (ответил, пусть и медленно).
func (s Status) OK() bool { return s == StatusOK || s == StatusSlow }

// Dialer — то, через что ходим (обычно *tunnel.Tunnel).
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Result — итог проверки одного DNS.
type Result struct {
	// Name — имя пресета (например 1.1.1.1) или произвольный IP.
	Name string
	// IP — фактический адрес, куда шёл запрос.
	IP string
	// Status — итог.
	Status Status
	// RTT — время отклика.
	RTT time.Duration
	// Err — ошибка, если есть.
	Err error
}

// Target — DNS-кандидат: имя для отображения и IP, куда слать запрос.
type Target struct {
	Name string
	IP   string
}

// Options — параметры проверки.
type Options struct {
	Timeout time.Duration
	Slow    time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Slow <= 0 {
		o.Slow = SlowThreshold
	}
	return o
}

// PresetTargets возвращает пресеты DNS, резолвя hostname'ы заранее (в поле DNS =
// WireGuard принимает только IP). Нерезолвящиеся пресеты пропускаются с ошибкой.
func PresetTargets() ([]Target, []Result) {
	var targets []Target
	var failed []Result
	for _, name := range config.PresetDNS {
		ip, err := config.ResolveDNS(name)
		if err != nil {
			failed = append(failed, Result{Name: name, Status: StatusError, Err: err})
			continue
		}
		targets = append(targets, Target{Name: name, IP: ip})
	}
	return targets, failed
}

// CheckAll проверяет все цели через переданный дозвонщик (туннель).
func CheckAll(ctx context.Context, d Dialer, targets []Target, opts Options) []Result {
	opts = opts.withDefaults()
	results := make([]Result, 0, len(targets))
	for _, t := range targets {
		results = append(results, Check(ctx, d, t, opts))
	}
	return results
}

// Check проверяет один DNS через туннель.
func Check(ctx context.Context, d Dialer, target Target, opts Options) Result {
	opts = opts.withDefaults()
	r := Result{Name: target.Name, IP: target.IP}

	if net.ParseIP(target.IP) == nil {
		r.Status = StatusError
		r.Err = fmt.Errorf("%q не является IP", target.IP)
		return r
	}

	query, id, err := buildQuery(QueryName)
	if err != nil {
		r.Status = StatusError
		r.Err = err
		return r
	}

	start := time.Now()
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(target.IP, "53"))
	if err != nil {
		r.Status = StatusError
		r.Err = fmt.Errorf("дозвон до %s: %w", target.IP, err)
		return r
	}
	defer conn.Close()

	deadline := start.Add(opts.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(query); err != nil {
		r.Status = StatusError
		r.Err = fmt.Errorf("отправка: %w", err)
		return r
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	r.RTT = time.Since(start)
	if err != nil {
		if isTimeout(err) {
			r.Status = StatusNoAnswer
			r.Err = fmt.Errorf("таймаут")
			return r
		}
		r.Status = StatusError
		r.Err = fmt.Errorf("чтение: %w", err)
		return r
	}

	answers, err := parseResponse(buf[:n], id)
	if err != nil {
		r.Status = StatusNoAnswer
		r.Err = err
		return r
	}
	if answers == 0 {
		r.Status = StatusNoAnswer
		r.Err = errors.New("ответ без адресов")
		return r
	}
	if r.RTT > opts.Slow {
		r.Status = StatusSlow
	} else {
		r.Status = StatusOK
	}
	return r
}

// buildQuery собирает DNS-запрос A для указанного имени.
func buildQuery(name string) ([]byte, uint16, error) {
	qname, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, 0, err
	}
	id := uint16(rand.Intn(1 << 16))
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  qname,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, 0, err
	}
	return packed, id, nil
}

// parseResponse разбирает ответ и возвращает число A-записей.
func parseResponse(raw []byte, id uint16) (int, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(raw); err != nil {
		return 0, fmt.Errorf("разбор ответа: %w", err)
	}
	if msg.Header.ID != id {
		return 0, fmt.Errorf("несовпадение id ответа")
	}
	if msg.RCode != dnsmessage.RCodeSuccess {
		return 0, fmt.Errorf("rcode %d", msg.RCode)
	}
	answers := 0
	for _, a := range msg.Answers {
		if a.Header.Type == dnsmessage.TypeA {
			answers++
		}
	}
	return answers, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// FormatTable — текстовая таблица результатов.
func FormatTable(results []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %-16s %-9s %-8s %s\n", "DNS", "IP", "СТАТУС", "RTT", "ПРИМЕЧАНИЕ")
	for _, r := range results {
		rtt := "?"
		if r.RTT > 0 {
			rtt = r.RTT.Round(time.Millisecond).String()
		}
		note := ""
		if r.Err != nil {
			note = r.Err.Error()
		}
		fmt.Fprintf(&b, "%-16s %-16s %-9s %-8s %s\n", r.Name, r.IP, r.Status, rtt, note)
	}
	return b.String()
}
