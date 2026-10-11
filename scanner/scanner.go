// Package scanner — перебор endpoint Cloudflare для WARP.
//
// Пайплайн: быстрый предфильтр (только WireGuard-хендшейк) → полная проверка
// через туннель (loc/colo из cdn-cgi/trace + замер задержки) → фильтр
// loc != RU без DME/LED/KJA → ранжирование по задержке.
package scanner

import (
	"context"
	"fmt"
	"math/rand"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexgen/warpgen/tunnel"
)

// DefaultPools — диапазоны Cloudflare, в которых живут WARP-endpoint'ы.
var DefaultPools = []string{
	"162.159.192.0/24",
	"162.159.193.0/24",
	"188.114.96.0/22",
}

// DefaultPorts — порты WARP.
var DefaultPorts = []int{500, 1701, 4500, 2408}

// ExcludedColos — российские colo Cloudflare: там блокировки по SNI+IP.
var ExcludedColos = map[string]bool{"DME": true, "LED": true, "KJA": true}

// Candidate — пара IP × порт, которую проверяем.
type Candidate struct {
	IP   netip.Addr
	Port int
}

// Endpoint — строковое представление "ip:port".
func (c Candidate) Endpoint() string {
	return tunnel.EndpointAddr(c.IP, c.Port)
}

// String реализует fmt.Stringer.
func (c Candidate) String() string { return c.Endpoint() }

// ParsePools разбирает список CIDR (или одиночных IP) в префиксы.
func ParsePools(specs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if p, err := netip.ParsePrefix(spec); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(spec); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		return nil, fmt.Errorf("некорректный диапазон %q: ожидается CIDR или IP", spec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("пустой список диапазонов")
	}
	return out, nil
}

// DefaultPrefixes — префиксы по умолчанию в распарсенном виде.
func DefaultPrefixes() []netip.Prefix {
	p, err := ParsePools(DefaultPools)
	if err != nil {
		panic(err)
	}
	return p
}

// Expand разворачивает префиксы в пары IP × порт.
func Expand(prefixes []netip.Prefix, ports []int) []Candidate {
	var out []Candidate
	for _, p := range prefixes {
		for a := p.Addr(); p.Contains(a); a = a.Next() {
			for _, port := range ports {
				out = append(out, Candidate{IP: a, Port: port})
			}
		}
	}
	return out
}

// Options — параметры скана.
type Options struct {
	// Concurrency — сколько туннелей работают параллельно.
	Concurrency int
	// MaxCandidates — сколько кандидатов проверять за прогон (0 → все).
	MaxCandidates int
	// TargetGood — остановиться, когда найдено столько подходящих endpoint.
	TargetGood int
	// HandshakeTimeout — таймаут хендшейка на кандидата.
	HandshakeTimeout time.Duration
	// TraceTimeout — таймаут запроса trace через туннель.
	TraceTimeout time.Duration
	// PingCount / PingTimeout — замер задержки внутри туннеля.
	PingCount   int
	PingTimeout time.Duration
	// SkipPing — не мерить задержку (только trace).
	SkipPing bool
	// OnProbe вызывается после каждого проверенного кандидата.
	OnProbe func(done, total int)
}

// Значения по умолчанию.
const (
	DefaultConcurrency      = 8
	DefaultHandshakeTimeout = 3 * time.Second
	DefaultTraceTimeout     = 5 * time.Second
	DefaultPingCount        = 5
	DefaultPingTimeout      = 2 * time.Second
)

func (o Options) withDefaults() Options {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if o.TraceTimeout <= 0 {
		o.TraceTimeout = DefaultTraceTimeout
	}
	if o.PingCount <= 0 {
		o.PingCount = DefaultPingCount
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = DefaultPingTimeout
	}
	return o
}

// Status — итог проверки одного endpoint.
type Status int

// Возможные статусы endpoint.
const (
	// StatusOK — endpoint подходит (есть хендшейк, trace, регион не RU).
	StatusOK Status = iota
	// StatusNoHandshake — хендшейк не прошёл (предфильтр отсёк).
	StatusNoHandshake
	// StatusNoTrace — хендшейк есть, но данные через туннель не идут.
	StatusNoTrace
	// StatusBadRegion — выходной регион RU.
	StatusBadRegion
	// StatusExcludedColo — российский colo (DME/LED/KJA).
	StatusExcludedColo
	// StatusTorn — туннель рвётся (срез ТСПУ).
	StatusTorn
)

// String — человекочитаемая метка статуса.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusNoHandshake:
		return "нет хендшейка"
	case StatusNoTrace:
		return "нет данных через туннель"
	case StatusBadRegion:
		return "регион RU"
	case StatusExcludedColo:
		return "исключённый colo"
	case StatusTorn:
		return "туннель рвётся"
	default:
		return "неизвестно"
	}
}

// Result — результат проверки одного кандидата.
type Result struct {
	Candidate
	// Loc — регион, который видит внешний сервис (SEEN AS).
	Loc string
	// Colo — узел Cloudflare (NODE).
	Colo string
	// RTT — средняя задержка внутри туннеля.
	RTT time.Duration
	// Loss — доля потерь внутри туннеля.
	Loss float32
	// Torn — признак среза ТСПУ.
	Torn bool
	// Status — итог.
	Status Status
}

// Qualifies — endpoint годится: есть данные, регион не RU, colo не исключён.
func Qualifies(r Result) bool {
	return r.Status == StatusOK
}

// ClassifyRegion решает статус по loc/colo.
func ClassifyRegion(loc, colo string) Status {
	if strings.EqualFold(strings.TrimSpace(loc), "RU") {
		return StatusBadRegion
	}
	if ExcludedColos[strings.ToUpper(strings.TrimSpace(colo))] {
		return StatusExcludedColo
	}
	return StatusOK
}

// Rank сортирует подходящие endpoint по возрастанию задержки.
func Rank(results []Result) []Result {
	good := make([]Result, 0, len(results))
	for _, r := range results {
		if Qualifies(r) {
			good = append(good, r)
		}
	}
	sort.SliceStable(good, func(i, j int) bool {
		return good[i].RTT < good[j].RTT
	})
	return good
}

// TunnelFactory создаёт новый туннель (нужен свой на каждого воркера).
type TunnelFactory func() (*tunnel.Tunnel, error)

// Scan прогоняет кандидатов через туннели: предфильтр хендшейком, затем trace и
// замер задержки. Возвращает все результаты в порядке проверки.
func Scan(ctx context.Context, newTunnel TunnelFactory, cands []Candidate, opts Options) ([]Result, error) {
	opts = opts.withDefaults()
	if opts.MaxCandidates > 0 && len(cands) > opts.MaxCandidates {
		cands = sample(cands, opts.MaxCandidates)
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("нет кандидатов для скана")
	}

	total := len(cands)
	work := make(chan Candidate)
	var (
		mu      sync.Mutex
		results []Result
		done    int
		good    int
		wg      sync.WaitGroup
	)

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := opts.Concurrency
	if workers > total {
		workers = total
	}
	var setupErr error
	var setupOnce sync.Once

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tn, err := newTunnel()
			if err != nil {
				setupOnce.Do(func() { setupErr = err })
				cancel()
				return
			}
			defer tn.Close()
			for c := range work {
				if scanCtx.Err() != nil {
					return
				}
				r := probe(scanCtx, tn, c, opts)
				mu.Lock()
				results = append(results, r)
				done++
				if Qualifies(r) {
					good++
				}
				if opts.OnProbe != nil {
					opts.OnProbe(done, total)
				}
				reached := opts.TargetGood > 0 && good >= opts.TargetGood
				mu.Unlock()
				if reached {
					cancel()
					return
				}
			}
		}()
	}

	go func() {
		defer close(work)
		for _, c := range cands {
			select {
			case <-scanCtx.Done():
				return
			case work <- c:
			}
		}
	}()

	wg.Wait()
	if setupErr != nil && len(results) == 0 {
		return nil, setupErr
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

// probe — проверка одного кандидата на уже поднятом туннеле.
func probe(ctx context.Context, tn *tunnel.Tunnel, c Candidate, opts Options) Result {
	r := Result{Candidate: c, Status: StatusNoHandshake}

	endpoint := c.Endpoint()
	if !tn.Handshake(ctx, endpoint, opts.HandshakeTimeout) {
		return r
	}
	if ctx.Err() != nil {
		return r
	}

	tr, err := tn.FetchTrace(ctx, opts.TraceTimeout)
	if err != nil {
		r.Status = StatusNoTrace
		return r
	}
	r.Loc, r.Colo = tr.Loc, tr.Colo
	r.Status = ClassifyRegion(tr.Loc, tr.Colo)
	if r.Status != StatusOK {
		return r
	}

	if !opts.SkipPing {
		pr := tn.Durability(tunnel.DefaultDNSAddr(), opts.PingCount, opts.PingTimeout)
		r.RTT, r.Loss, r.Torn = pr.RTT, pr.Loss, pr.Torn
		if pr.Torn {
			r.Status = StatusTorn
		}
	}
	return r
}

func sample(cands []Candidate, n int) []Candidate {
	if n <= 0 || n >= len(cands) {
		return cands
	}
	out := make([]Candidate, n)
	for i, j := range rand.Perm(len(cands))[:n] {
		out[i] = cands[j]
	}
	return out
}

// FormatTable — простая текстовая таблица результатов.
func FormatTable(results []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-6s %-4s %-6s %-8s %-7s %s\n",
		"ENDPOINT", "SEEN AS", "NODE", "RTT", "LOSS", "STATUS", "")
	for _, r := range results {
		rtt := "?"
		if r.RTT > 0 {
			rtt = strconv.Itoa(int(r.RTT.Milliseconds())) + "ms"
		}
		loss := fmt.Sprintf("%.0f%%", r.Loss*100)
		fmt.Fprintf(&b, "%-24s %-6s %-4s %-6s %-8s %-7s\n",
			r.Endpoint(), dash(r.Loc), dash(r.Colo), rtt, loss, r.Status)
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
