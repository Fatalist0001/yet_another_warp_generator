package core

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Коды и константы SOCKS5 (RFC 1928).
const (
	socksVersion    = 5
	socksNoAuth     = 0
	socksCmdConnect = 1
	socksAtypIPv4   = 1
	socksAtypName   = 3
	socksAtypIPv6   = 4
	socksRepOK      = 0
	socksRepDenied  = 2
	socksRepHost    = 4
	socksRepCmd     = 7
	socksRepAtyp    = 8
)

// Значения по умолчанию для локального SOCKS5-сервера.
const (
	// DefaultSocksListen — адрес SOCKS5 в режиме без Psiphon.
	DefaultSocksListen = "127.0.0.1:8086"
	// DefaultSocksHandshake — предел на обмен приветствием и запросом CONNECT.
	DefaultSocksHandshake = 10 * time.Second
	// DefaultSocksDial — предел на соединение через туннель.
	DefaultSocksDial = 15 * time.Second
)

// SocksProxy — локальный SOCKS5-сервер без аутентификации: принимает CONNECT и
// открывает соединение через userspace-туннель. Это режим без Psiphon: наружу
// отдаётся сам WARP-туннель с обфускацией AWG, без warp-plus.
//
// Туннель можно менять на лету (SetTunnel): при failover мост системного туннеля
// продолжает ходить на тот же адрес SOCKS5, а сервер направляет новые соединения
// в новый туннель.
type SocksProxy struct {
	opts SocksOptions
	ln   net.Listener

	mu     sync.Mutex
	tunnel Dialer
	conns  map[net.Conn]struct{}
	closed bool
}

// Dialer — минимальный источник соединений через туннель (*tunnel.Tunnel и
// подделки в тестах).
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// SocksOptions — параметры локального SOCKS5-сервера.
type SocksOptions struct {
	// Listen — адрес прослушивания (только loopback, порт 0 — свободный).
	Listen string
	// HandshakeTimeout — предел на приветствие и запрос CONNECT.
	HandshakeTimeout time.Duration
	// DialTimeout — предел на соединение через туннель.
	DialTimeout time.Duration
	// Logf — приёмник служебных сообщений (nil — молча).
	Logf func(format string, args ...any)
}

// NewSocksProxy занимает локальный TCP-порт. Слушаем только loopback: прокси
// отдаёт наружу поднятый туннель без аутентификации.
func NewSocksProxy(opts SocksOptions) (*SocksProxy, error) {
	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		listen = DefaultSocksListen
	}
	ap, err := netip.ParseAddrPort(listen)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5: некорректный адрес прослушивания %q: ожидается ip:port", opts.Listen)
	}
	if !ap.Addr().IsLoopback() {
		return nil, fmt.Errorf("SOCKS5: адрес %s не локальный — слушаем только loopback", listen)
	}
	ln, err := net.Listen("tcp", ap.String())
	if err != nil {
		return nil, fmt.Errorf("SOCKS5: сокет %s: %w", listen, err)
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultSocksHandshake
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultSocksDial
	}
	return &SocksProxy{opts: opts, ln: ln, conns: make(map[net.Conn]struct{})}, nil
}

// Addr — фактический адрес прослушивания.
func (p *SocksProxy) Addr() string {
	if p == nil || p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// SetTunnel задаёт туннель для новых соединений (nil — отсоединить: новые
// соединения получают отказ, уже установленные не рвутся).
func (p *SocksProxy) SetTunnel(t Dialer) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.tunnel = t
	p.mu.Unlock()
}

// Serve принимает соединения до отмены ctx или Close. Возвращает nil при штатной
// остановке.
func (p *SocksProxy) Serve(ctx context.Context) error {
	done := make(chan struct{})
	defer close(done)

	// Отмена контекста должна разбудить блокирующий Accept.
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-done:
		}
	}()

	for {
		conn, err := p.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("SOCKS5: accept: %w", err)
		}
		if !p.track(conn) {
			_ = conn.Close()
			continue
		}
		go p.serveConn(ctx, conn)
	}
}

// Close закрывает слушателя и все активные соединения. Повторный вызов безопасен.
func (p *SocksProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	var conns []net.Conn
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.conns = make(map[net.Conn]struct{})
	p.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
	if p.ln == nil {
		return nil
	}
	return p.ln.Close()
}

// track ставит соединение на учёт (false — прокси уже закрыт).
func (p *SocksProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

// untrack снимает соединение с учёта.
func (p *SocksProxy) untrack(conn net.Conn) {
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}

// serveConn обслуживает одно соединение: приветствие, CONNECT и двусторонний
// обмен данными. Все обращения к сети ограничены таймаутами.
func (p *SocksProxy) serveConn(ctx context.Context, conn net.Conn) {
	defer func() {
		p.untrack(conn)
		_ = conn.Close()
	}()

	_ = conn.SetDeadline(time.Now().Add(p.opts.HandshakeTimeout))
	target, err := p.handshake(conn)
	if err != nil {
		if err != errSilent {
			p.log("SOCKS5: %v", err)
		}
		return
	}

	up, err := p.dial(ctx, target)
	if err != nil {
		p.log("SOCKS5: %s: %v", target, err)
		_ = writeSocksReply(conn, socksRepHost)
		return
	}
	defer up.Close()

	if err := writeSocksReply(conn, socksRepOK); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	relay(conn, up)
}

// handshake читает приветствие и запрос CONNECT, отвечает на приветствие и
// возвращает целевой адрес (host:port).
func (p *SocksProxy) handshake(conn net.Conn) (string, error) {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return "", fmt.Errorf("приветствие: %w", err)
	}
	if head[0] != socksVersion {
		return "", fmt.Errorf("версия %d вместо %d", head[0], socksVersion)
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("список методов: %w", err)
	}
	if !hasNoAuth(methods) {
		_, _ = conn.Write([]byte{socksVersion, 0xFF})
		return "", errSilent
	}
	if _, err := conn.Write([]byte{socksVersion, socksNoAuth}); err != nil {
		return "", fmt.Errorf("ответ на приветствие: %w", err)
	}

	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return "", fmt.Errorf("запрос: %w", err)
	}
	if req[0] != socksVersion {
		_ = writeSocksReply(conn, socksRepDenied)
		return "", fmt.Errorf("версия запроса %d вместо %d", req[0], socksVersion)
	}
	if req[1] != socksCmdConnect {
		_ = writeSocksReply(conn, socksRepCmd)
		return "", fmt.Errorf("команда %d не поддерживается (есть только CONNECT)", req[1])
	}
	host, err := readSocksHost(conn, req[3])
	if err != nil {
		_ = writeSocksReply(conn, socksRepAtyp)
		return "", err
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		_ = writeSocksReply(conn, socksRepDenied)
		return "", fmt.Errorf("порт: %w", err)
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if port == 0 {
		_ = writeSocksReply(conn, socksRepDenied)
		return "", errors.New("нулевой порт")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// dial открывает соединение через текущий туннель (hostname резолвится внутри
// туннеля — netstack).
func (p *SocksProxy) dial(ctx context.Context, target string) (net.Conn, error) {
	p.mu.Lock()
	tn := p.tunnel
	p.mu.Unlock()
	if tn == nil {
		return nil, errors.New("туннель ещё не готов")
	}
	dctx, cancel := context.WithTimeout(ctx, p.opts.DialTimeout)
	defer cancel()
	return tn.DialContext(dctx, "tcp", target)
}

func (p *SocksProxy) log(format string, args ...any) {
	if p.opts.Logf != nil {
		p.opts.Logf(format, args...)
	}
}

// errSilent — отказ уже объяснён ответом клиенту, логировать нечего.
var errSilent = errors.New("SOCKS5: соединение отклонено")

// hasNoAuth — клиент предлагает метод «без аутентификации».
func hasNoAuth(methods []byte) bool {
	for _, m := range methods {
		if m == socksNoAuth {
			return true
		}
	}
	return false
}

// readSocksHost читает адрес запроса по типу ATYP.
func readSocksHost(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", fmt.Errorf("адрес IPv4: %w", err)
		}
		return netip.AddrFrom4(b).String(), nil
	case socksAtypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return "", fmt.Errorf("адрес IPv6: %w", err)
		}
		return netip.AddrFrom16(b).String(), nil
	case socksAtypName:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", fmt.Errorf("длина имени: %w", err)
		}
		if length[0] == 0 {
			return "", errors.New("пустое имя хоста")
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", fmt.Errorf("имя хоста: %w", err)
		}
		return string(name), nil
	default:
		return "", fmt.Errorf("неизвестный тип адреса %d", atyp)
	}
}

// writeSocksReply отправляет ответ CONNECT с пустым привязанным адресом.
func writeSocksReply(conn net.Conn, rep byte) error {
	_, err := conn.Write([]byte{socksVersion, rep, 0, socksAtypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// relay копирует данные в обе стороны, передавая полузакрытие (CloseWrite):
// серверы, ждущие EOF, не повиснут до таймаута простоя.
func relay(client, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, client)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, up)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

// closeWrite полузакрывает соединение, если оно это умеет.
func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
