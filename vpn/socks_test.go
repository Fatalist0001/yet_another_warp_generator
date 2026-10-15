package vpn

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawSocks — минимальный SOCKS5-сервер для проверки отказов: отдаёт заранее
// заданные ответы и больше ничего не делает.
func rawSocks(t *testing.T, greeting []byte, reply []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("сервер SOCKS5: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var head [2]byte
				if _, err := io.ReadFull(c, head[:]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, make([]byte, int(head[1]))); err != nil {
					return
				}
				if _, err := c.Write(greeting); err != nil {
					return
				}
				if reply == nil {
					_, _ = io.Copy(io.Discard, c)
					return
				}
				// Ждём запрос CONNECT (IPv4: 4 + 4 + 2 байта) и отвечаем отказом.
				if _, err := io.ReadFull(c, make([]byte, 10)); err != nil {
					return
				}
				_, _ = c.Write(reply)
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestDialSocks5ProtectsSocket(t *testing.T) {
	echo := tcpEcho(t)
	socks := newFakeSOCKS5(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	var mu sync.Mutex
	var fds []int
	protect := func(fd int) error {
		mu.Lock()
		fds = append(fds, fd)
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialSocks5(ctx, socks.addr(), "10.20.30.40:80", protect)
	if err != nil {
		t.Fatalf("dialSocks5: %v", err)
	}
	defer conn.Close()

	mu.Lock()
	got := append([]int(nil), fds...)
	mu.Unlock()
	if len(got) != 1 || got[0] < 3 {
		t.Fatalf("Protect получил дескрипторы %v, ожидали один существующий fd", got)
	}

	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(buf[:n]) != "ok" {
		t.Errorf("вернулось %q", buf[:n])
	}
}

func TestDialSocks5ProtectError(t *testing.T) {
	echo := tcpEcho(t)
	socks := newFakeSOCKS5(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	_, err := dialSocks5(context.Background(), socks.addr(), "10.20.30.40:80", func(int) error {
		return errors.New("VpnService отказал")
	})
	if err == nil {
		t.Fatal("ожидали ошибку защиты сокета")
	}
	if !strings.Contains(err.Error(), "защита сокета") {
		t.Errorf("текст ошибки = %q", err)
	}
}

func TestDialSocks5AuthRequired(t *testing.T) {
	addr := rawSocks(t, []byte{5, 2}, nil)
	_, err := dialSocks5(context.Background(), addr, "10.0.0.1:80", nil)
	if err == nil {
		t.Fatal("ожидали ошибку про аутентификацию")
	}
	if !strings.Contains(err.Error(), "аутентификацию") {
		t.Errorf("текст ошибки = %q", err)
	}
}

func TestDialSocks5Refused(t *testing.T) {
	addr := rawSocks(t, []byte{5, 0}, []byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
	_, err := dialSocks5(context.Background(), addr, "10.0.0.1:80", nil)
	if err == nil {
		t.Fatal("ожидали отказ прокси")
	}
	if !strings.Contains(err.Error(), "отклонено") {
		t.Errorf("текст ошибки = %q", err)
	}
}

func TestDialSocks5BadAddress(t *testing.T) {
	if _, err := dialSocks5(context.Background(), "127.0.0.1:1", "не-адрес", nil); err == nil {
		t.Fatal("ожидали ошибку разбора адреса")
	}
}

func TestSocksRequestAddressTypes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		atyp byte
		tail string
	}{
		{"IPv4", "10.1.2.3:80", 1, ""},
		{"домен", "example.com:443", 3, "example.com"},
		{"IPv6", "[2606:4700::1111]:53", 4, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := socksRequest(c.in)
			if err != nil {
				t.Fatalf("socksRequest: %v", err)
			}
			if len(req) < 10 || req[0] != 5 || req[1] != 1 {
				t.Fatalf("запрос = % x", req)
			}
			if req[3] != c.atyp {
				t.Errorf("тип адреса = %d, ожидали %d", req[3], c.atyp)
			}
			if c.tail != "" && !strings.Contains(string(req), c.tail) {
				t.Errorf("в запросе нет имени %q: % x", c.tail, req)
			}
		})
	}
}

func TestSocksRequestErrors(t *testing.T) {
	for _, bad := range []string{"", "10.0.0.1", "10.0.0.1:0", "10.0.0.1:70000", "10.0.0.1:abc", strings.Repeat("a", 300) + ":80"} {
		if _, err := socksRequest(bad); err == nil {
			t.Errorf("%q: ожидали ошибку", bad)
		}
	}
}
