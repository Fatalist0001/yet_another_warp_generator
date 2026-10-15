package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Forwarder — UDP-форвардер: принимает пакеты на локальном loopback-адресе и
// передаёт их через userspace-туннель на целевой endpoint (внутренний WARP), как
// обычный клиент WARP. Пакеты копируются в обе стороны без разбора.
//
// Так внешний туннель становится доступен не только этому процессу: warp-plus
// получает адрес форвардера в -e и думает, что говорит напрямую с Cloudflare.
type Forwarder struct {
	tunnel Tunnel
	target string
	mtu    int
	ttl    time.Duration
	limit  int
	logf   func(format string, args ...any)

	conn *net.UDPConn

	mu       sync.Mutex
	sessions map[string]*session
	stats    ForwarderStats
	closed   bool
}

// session — сессия одного локального источника: UDP-соединение через туннель.
type session struct {
	src  netip.AddrPort
	conn net.Conn
	// last — время последней активности (под защитой Forwarder.mu).
	last time.Time
}

// ForwarderStats — счётчики форвардера (для диагностики и тестов).
type ForwarderStats struct {
	// Sessions — сколько сессий сейчас живо.
	Sessions int
	// PacketsFromClient / PacketsFromTunnel — сколько пакетов прошло в каждую сторону.
	PacketsFromClient int
	PacketsFromTunnel int
	// Oversize — пакеты длиннее MTU внешнего туннеля (передаём как есть).
	Oversize int
	// Dropped — пакеты, для которых сессию создать не удалось (лимит сессий).
	Dropped int
}

// Значения по умолчанию и пределы форвардера.
const (
	// DefaultMTU — MTU внешнего туннеля (WARP-safe значение).
	DefaultMTU = 1280
	// DefaultSessionTTL — сколько живёт простаивающая сессия.
	DefaultSessionTTL = 120 * time.Second
	// DefaultMaxSessions — предел одновременных сессий.
	DefaultMaxSessions = 32
	// maxPacketSize — размер буфера под один UDP-пакет.
	maxPacketSize = 64 << 10
	// minSweepInterval / maxSweepInterval — как часто подчищаем простаивающие сессии.
	minSweepInterval = 50 * time.Millisecond
	maxSweepInterval = 5 * time.Second
)

// ForwarderOptions — параметры форвардера.
type ForwarderOptions struct {
	// Listen — локальный адрес прослушивания (только loopback, порт 0 — свободный).
	Listen string
	// Target — ip:port внутреннего WARP, куда уходят пакеты.
	Target string
	// MTU — MTU внешнего туннеля (пакеты больше считаем как oversize).
	MTU int
	// SessionTTL — простой сессии до закрытия.
	SessionTTL time.Duration
	// MaxSessions — предел одновременных сессий.
	MaxSessions int
	// Logf — приёмник служебных сообщений. nil — молча.
	Logf func(format string, args ...any)
}

// NewForwarder проверяет параметры и сразу занимает локальный UDP-порт, чтобы
// вызывающая сторона узнала фактический адрес (Addr) до запуска Run.
func NewForwarder(tn Tunnel, opts ForwarderOptions) (*Forwarder, error) {
	if tn == nil {
		return nil, errors.New("форвардер: не задан туннель")
	}
	target := strings.TrimSpace(opts.Target)
	if _, err := netip.ParseAddrPort(target); err != nil {
		return nil, fmt.Errorf("форвардер: некорректный целевой адрес %q: ожидается ip:port", opts.Target)
	}

	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		listen = DefaultListen
	}
	ap, err := netip.ParseAddrPort(listen)
	if err != nil {
		return nil, fmt.Errorf("форвардер: некорректный адрес прослушивания %q: ожидается ip:port", opts.Listen)
	}
	if !ap.Addr().IsLoopback() {
		// Форвардер отдаёт наружу поднятый туннель без аутентификации, поэтому
		// слушаем только loopback.
		return nil, fmt.Errorf("форвардер: адрес %s не локальный — слушаем только loopback", listen)
	}

	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(ap))
	if err != nil {
		return nil, fmt.Errorf("форвардер: сокет %s: %w", listen, err)
	}

	mtu := opts.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	limit := opts.MaxSessions
	if limit <= 0 {
		limit = DefaultMaxSessions
	}
	return &Forwarder{
		tunnel:   tn,
		target:   target,
		mtu:      mtu,
		ttl:      ttl,
		limit:    limit,
		logf:     opts.Logf,
		conn:     conn,
		sessions: make(map[string]*session),
	}, nil
}

// Addr — фактический локальный адрес форвардера.
func (f *Forwarder) Addr() string {
	if f.conn == nil {
		return ""
	}
	return f.conn.LocalAddr().String()
}

// Stats — снимок счётчиков форвардера.
func (f *Forwarder) Stats() ForwarderStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

// Run принимает пакеты до отмены ctx (или ошибки сокета) и по завершении закрывает
// сокет и все сессии. Возвращает nil при штатной остановке по ctx.
func (f *Forwarder) Run(ctx context.Context) error {
	done := make(chan struct{})
	defer close(done)

	// Отмена контекста должна разбудить блокирующее чтение сокета.
	go func() {
		select {
		case <-ctx.Done():
			_ = f.Close()
		case <-done:
		}
	}()
	go f.sweepLoop(ctx, done)

	buf := make([]byte, maxPacketSize)
	for {
		n, src, err := f.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("форвардер: чтение сокета: %w", err)
		}
		f.handle(ctx, buf[:n], src)
	}
}

// Close закрывает сокет и все сессии. Повторный вызов безопасен.
func (f *Forwarder) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	sessions := make([]*session, 0, len(f.sessions))
	for _, s := range f.sessions {
		sessions = append(sessions, s)
	}
	f.sessions = make(map[string]*session)
	f.stats.Sessions = 0
	f.mu.Unlock()

	for _, s := range sessions {
		_ = s.conn.Close()
	}
	if f.conn == nil {
		return nil
	}
	return f.conn.Close()
}

// handle передаёт один пакет клиента в туннель, создавая сессию при необходимости.
func (f *Forwarder) handle(ctx context.Context, pkt []byte, src netip.AddrPort) {
	if len(pkt) > f.mtu {
		f.mu.Lock()
		f.stats.Oversize++
		f.mu.Unlock()
		f.log("oversize: пакет %d байт больше MTU %d — передаём как есть", len(pkt), f.mtu)
	}

	sess, err := f.session(ctx, src)
	if err != nil {
		if ctx.Err() == nil {
			f.log("форвардер: сессия %s не создана: %v", src, err)
		}
		return
	}
	if _, err := sess.conn.Write(pkt); err != nil {
		f.forget(sess)
		return
	}
	f.mu.Lock()
	sess.last = time.Now()
	f.stats.PacketsFromClient++
	f.mu.Unlock()
}

// session возвращает сессию источника, создавая её при необходимости. Пакеты одного
// источника обрабатываются в одном цикле чтения, поэтому гонки при создании нет.
func (f *Forwarder) session(ctx context.Context, src netip.AddrPort) (*session, error) {
	key := src.String()

	f.mu.Lock()
	if s, ok := f.sessions[key]; ok {
		f.mu.Unlock()
		return s, nil
	}
	if len(f.sessions) >= f.limit {
		f.stats.Dropped++
		f.mu.Unlock()
		return nil, fmt.Errorf("достигнут лимит сессий (%d)", f.limit)
	}
	f.mu.Unlock()

	conn, err := f.tunnel.DialContext(ctx, "udp", f.target)
	if err != nil {
		return nil, fmt.Errorf("UDP через туннель: %w", err)
	}
	s := &session{src: src, conn: conn, last: time.Now()}

	f.mu.Lock()
	f.sessions[key] = s
	f.stats.Sessions = len(f.sessions)
	f.mu.Unlock()

	go f.pump(s)
	return s, nil
}

// pump копирует пакеты из туннеля обратно клиенту и завершает сессию при ошибке.
func (f *Forwarder) pump(s *session) {
	buf := make([]byte, maxPacketSize)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			f.forget(s)
			return
		}
		if _, err := f.conn.WriteToUDPAddrPort(buf[:n], s.src); err != nil {
			f.forget(s)
			return
		}
		f.mu.Lock()
		s.last = time.Now()
		f.stats.PacketsFromTunnel++
		f.mu.Unlock()
	}
}

// forget убирает сессию из таблицы и закрывает её соединение.
func (f *Forwarder) forget(s *session) {
	f.mu.Lock()
	key := s.src.String()
	if cur, ok := f.sessions[key]; ok && cur == s {
		delete(f.sessions, key)
	}
	f.stats.Sessions = len(f.sessions)
	f.mu.Unlock()
	_ = s.conn.Close()
}

// sweepLoop периодически закрывает простаивающие сессии (и умирает вместе с Run).
func (f *Forwarder) sweepLoop(ctx context.Context, done <-chan struct{}) {
	interval := f.ttl / 4
	if interval < minSweepInterval {
		interval = minSweepInterval
	}
	if interval > maxSweepInterval {
		interval = maxSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			f.closeIdle()
		}
	}
}

// closeIdle закрывает сессии, простаивавшие дольше SessionTTL.
func (f *Forwarder) closeIdle() {
	now := time.Now()
	f.mu.Lock()
	var idle []*session
	for _, s := range f.sessions {
		if now.Sub(s.last) > f.ttl {
			idle = append(idle, s)
		}
	}
	f.mu.Unlock()

	for _, s := range idle {
		f.log("форвардер: сессия %s простаивала больше %s — закрываем", s.src, f.ttl)
		f.forget(s)
	}
}

func (f *Forwarder) log(format string, args ...any) {
	if f.logf != nil {
		f.logf(format, args...)
	}
}
