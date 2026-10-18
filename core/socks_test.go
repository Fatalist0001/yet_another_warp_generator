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
	"testing"
	"time"
)

// echoDialer — поддельный туннель: любое соединение уходит на локальное TCP-эхо.
type echoDialer struct {
	addr string

	mu     sync.Mutex
	target string
	calls  int
}

func (d *echoDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	d.target = address
	d.mu.Unlock()
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("ожидали tcp, а не %q", network)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.addr)
}

func (d *echoDialer) lastTarget() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.target
}

// tcpEcho — локальное TCP-эхо на loopback.
func tcpEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("TCP-эхо: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

// startProxy поднимает SOCKS5-сервер на loopback с заданным туннелем.
func startProxy(t *testing.T, dial Dialer) *SocksProxy {
	t.Helper()
	p, err := NewSocksProxy(SocksOptions{Listen: "127.0.0.1:0", Logf: t.Logf})
	if err != nil {
		t.Fatalf("NewSocksProxy: %v", err)
	}
	p.SetTunnel(dial)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = p.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve не завершился после отмены контекста")
		}
	})
	return p
}

// Не-loopback адрес отвергается: прокси отдаёт туннель без аутентификации.
func TestSocksProxyRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "10.0.0.1:8086"} {
		if _, err := NewSocksProxy(SocksOptions{Listen: addr}); err == nil {
			t.Errorf("адрес %s должен быть отвергнут", addr)
		}
	}
}

// CONNECT проксируется в туннель, данные ходят в обе стороны.
func TestSocksProxyRelaysConnect(t *testing.T) {
	dial := &echoDialer{addr: tcpEcho(t)}
	p := startProxy(t, dial)

	conn := socksConnect(t, p.Addr(), dial.addr)
	defer conn.Close()

	if _, err := conn.Write([]byte("привет")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(buf[:n]) != "привет" {
		t.Errorf("вернулось %q", buf[:n])
	}
	if got := dial.lastTarget(); got != dial.addr {
		t.Errorf("туннель получил %q, ожидали %q", got, dial.addr)
	}
}

// Команда, отличная от CONNECT, отвергается ответом 0x07.
func TestSocksProxyRejectsOtherCommands(t *testing.T) {
	dial := &echoDialer{addr: tcpEcho(t)}
	p := startProxy(t, dial)

	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("соединение с прокси: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		t.Fatalf("приветствие: %v", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(conn, greet[:]); err != nil {
		t.Fatalf("ответ на приветствие: %v", err)
	}
	// BIND (команда 2) на адрес 127.0.0.1:80.
	req := []byte{socksVersion, 2, 0, socksAtypIPv4, 127, 0, 0, 1, 0, 80}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("запрос: %v", err)
	}
	rep, err := readSocksReplyCode(conn)
	if err != nil {
		t.Fatalf("ответ: %v", err)
	}
	if rep != socksRepCmd {
		t.Errorf("код ответа %d, ожидали %d", rep, socksRepCmd)
	}
}

// Клиент без метода «без аутентификации» получает отказ 0xFF.
func TestSocksProxyRequiresNoAuth(t *testing.T) {
	p := startProxy(t, &echoDialer{addr: tcpEcho(t)})
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("соединение с прокси: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{socksVersion, 1, 2}); err != nil {
		t.Fatalf("приветствие: %v", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(conn, greet[:]); err != nil {
		t.Fatalf("ответ: %v", err)
	}
	if greet[0] != socksVersion || greet[1] != 0xFF {
		t.Errorf("ответ на приветствие %v, ожидали {5, 255}", greet)
	}
}

// Без привязанного туннеля новые соединения получают отказ.
func TestSocksProxyWithoutTunnel(t *testing.T) {
	p := startProxy(t, nil)
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("соединение с прокси: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		t.Fatalf("приветствие: %v", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(conn, greet[:]); err != nil {
		t.Fatalf("ответ на приветствие: %v", err)
	}
	req, err := socksRequestFor("127.0.0.1:80")
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("запрос: %v", err)
	}
	rep, err := readSocksReplyCode(conn)
	if err != nil {
		t.Fatalf("ответ: %v", err)
	}
	if rep != socksRepHost {
		t.Errorf("код ответа %d, ожидали %d", rep, socksRepHost)
	}
}

// Close обрывает обслуживание: Serve возвращается, клиент отваливается.
func TestSocksProxyCloseStopsServe(t *testing.T) {
	p, err := NewSocksProxy(SocksOptions{Listen: "127.0.0.1:0", HandshakeTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewSocksProxy: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Serve(context.Background()) }()

	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("соединение: %v", err)
	}
	defer conn.Close()

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve не завершился после Close")
	}
}

// --- хелперы SOCKS5-клиента ---

// socksConnect выполняет CONNECT через прокси и возвращает готовое соединение.
func socksConnect(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("SOCKS5 %s: %v", proxyAddr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		conn.Close()
		t.Fatalf("приветствие: %v", err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(conn, greet[:]); err != nil {
		conn.Close()
		t.Fatalf("ответ на приветствие: %v", err)
	}
	if greet[0] != socksVersion || greet[1] != socksNoAuth {
		conn.Close()
		t.Fatalf("приветствие отклонено: %v", greet)
	}

	req, err := socksRequestFor(target)
	if err != nil {
		conn.Close()
		t.Fatalf("запрос: %v", err)
	}
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		t.Fatalf("запрос CONNECT: %v", err)
	}
	rep, err := readSocksReplyCode(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("ответ на CONNECT: %v", err)
	}
	if rep != socksRepOK {
		conn.Close()
		t.Fatalf("CONNECT отклонён, код %d", rep)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

// socksRequestFor собирает запрос CONNECT для адреса ip:port.
func socksRequestFor(target string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("некорректный порт %q", portStr)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	if !addr.Is4() {
		return nil, errors.New("тестовый клиент умеет только IPv4")
	}
	out := []byte{socksVersion, socksCmdConnect, 0, socksAtypIPv4}
	a4 := addr.As4()
	out = append(out, a4[:]...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(p))
	return append(out, pb[:]...), nil
}

// readSocksReplyCode читает ответ прокси и возвращает код.
func readSocksReplyCode(conn net.Conn) (byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return 0, err
	}
	if head[0] != socksVersion {
		return 0, fmt.Errorf("версия ответа %d", head[0])
	}
	if err := skipReplyAddr(conn, head[3]); err != nil {
		return 0, err
	}
	return head[1], nil
}

func skipReplyAddr(conn net.Conn, atyp byte) error {
	switch atyp {
	case socksAtypIPv4:
		_, err := io.ReadFull(conn, make([]byte, 4))
		if err != nil {
			return err
		}
	case socksAtypIPv6:
		_, err := io.ReadFull(conn, make([]byte, 16))
		if err != nil {
			return err
		}
	case socksAtypName:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		if _, err := io.ReadFull(conn, make([]byte, int(length[0]))); err != nil {
			return err
		}
	default:
		return fmt.Errorf("неизвестный тип адреса %d", atyp)
	}
	_, err := io.ReadFull(conn, make([]byte, 2))
	return err
}
