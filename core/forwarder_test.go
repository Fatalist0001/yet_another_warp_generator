package core

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// dialForwarder открывает UDP-сокет клиента форвардера (новый источник).
func dialForwarder(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("клиент форвардера: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn.(*net.UDPConn)
}

// roundTrip отправляет датаграмму через уже открытый сокет и ждёт ответ.
func roundTrip(conn *net.UDPConn, payload []byte, timeout time.Duration) ([]byte, error) {
	if _, err := conn.Write(payload); err != nil {
		return nil, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	buf := make([]byte, maxPacketSize)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// echoOnce отправляет одну датаграмму с нового источника и ждёт ответ.
func echoOnce(t *testing.T, addr string, payload []byte, timeout time.Duration) ([]byte, error) {
	t.Helper()
	return roundTrip(dialForwarder(t, addr), payload, timeout)
}

func TestForwarderRelaysThroughTunnel(t *testing.T) {
	echo := udpEcho(t)
	fleet := &tunnelFleet{echo: echo}

	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{
		Listen: "127.0.0.1:0", Target: "162.159.192.1:2408",
	})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	got, err := echoOnce(t, f.Addr(), []byte("ping"), 3*time.Second)
	if err != nil {
		t.Fatalf("ответа от эха нет: %v", err)
	}
	if string(got) != "ping" {
		t.Errorf("эхо вернуло %q, ожидали \"ping\"", got)
	}

	stats := f.Stats()
	if stats.Sessions != 1 {
		t.Errorf("сессий = %d, ожидали 1", stats.Sessions)
	}
	if stats.PacketsFromClient != 1 || stats.PacketsFromTunnel != 1 {
		t.Errorf("пакетов клиент→туннель = %d, туннель→клиент = %d, ожидали по 1",
			stats.PacketsFromClient, stats.PacketsFromTunnel)
	}
}

// Второй пакет идёт в ту же сессию, а не создаёт новую.
func TestForwarderReusesSession(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{Listen: "127.0.0.1:0", Target: "162.159.192.1:2408"})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = f.Run(ctx) }()
	defer func() { cancel(); _ = f.Close() }()

	conn := dialForwarder(t, f.Addr())
	for i := 0; i < 3; i++ {
		if _, err := roundTrip(conn, []byte("ping"), 3*time.Second); err != nil {
			t.Fatalf("пакет %d: %v", i, err)
		}
	}
	if s := f.Stats(); s.Sessions != 1 {
		t.Errorf("сессий = %d, ожидали 1 (сессия переиспользуется)", s.Sessions)
	}
	if s := f.Stats(); s.PacketsFromClient != 3 {
		t.Errorf("пакетов от клиента = %d, ожидали 3", s.PacketsFromClient)
	}
}

// Пакет длиннее MTU внешнего туннеля не роняет процесс: считается как oversize и
// передаётся как есть (реальное поведение netstack проверяется в e2e).
func TestForwarderOversizeCounted(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	var logs []string
	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{
		Listen: "127.0.0.1:0", Target: "162.159.192.1:2408", MTU: 64,
		Logf: func(format string, args ...any) { logs = append(logs, format) },
	})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = f.Run(ctx) }()
	defer func() { cancel(); _ = f.Close() }()

	small := []byte("ok")
	if _, err := echoOnce(t, f.Addr(), small, 3*time.Second); err != nil {
		t.Fatalf("обычный пакет: %v", err)
	}
	big := bytes.Repeat([]byte("x"), 200)
	got, err := echoOnce(t, f.Addr(), big, 3*time.Second)
	if err != nil {
		t.Fatalf("большой пакет: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Errorf("большой пакет вернулся искажённым (%d байт)", len(got))
	}

	if s := f.Stats(); s.Oversize != 1 {
		t.Errorf("oversize = %d, ожидали 1", s.Oversize)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "oversize") {
			found = true
		}
	}
	if !found {
		t.Errorf("oversize не залогирован: %v", logs)
	}
}

// Простаивающая сессия закрывается по SessionTTL.
func TestForwarderClosesIdleSession(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{
		Listen: "127.0.0.1:0", Target: "162.159.192.1:2408", SessionTTL: 80 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = f.Run(ctx) }()
	defer func() { cancel(); _ = f.Close() }()

	if _, err := echoOnce(t, f.Addr(), []byte("ping"), 3*time.Second); err != nil {
		t.Fatalf("пакет: %v", err)
	}
	if s := f.Stats(); s.Sessions != 1 {
		t.Fatalf("сессий = %d, ожидали 1", s.Sessions)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.Stats().Sessions == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("простаивающая сессия не закрылась за 3s (TTL 80ms)")
}

// Число сессий ограничено: пакет нового источника отбрасывается.
func TestForwarderSessionLimit(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{
		Listen: "127.0.0.1:0", Target: "162.159.192.1:2408", MaxSessions: 1,
	})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = f.Run(ctx) }()
	defer func() { cancel(); _ = f.Close() }()

	if _, err := echoOnce(t, f.Addr(), []byte("первый"), 3*time.Second); err != nil {
		t.Fatalf("первый источник: %v", err)
	}
	// Второй источник — это уже другая сессия, лимит не пускает.
	if _, err := echoOnce(t, f.Addr(), []byte("второй"), 500*time.Millisecond); err == nil {
		t.Error("пакет второго источника не должен проходить при лимите 1")
	}
	stats := f.Stats()
	if stats.Dropped != 1 {
		t.Errorf("dropped = %d, ожидали 1", stats.Dropped)
	}
	if stats.Sessions != 1 {
		t.Errorf("сессий = %d, ожидали 1", stats.Sessions)
	}
}

// Отмена ctx закрывает сокет и сессии, Run возвращает nil без утечек.
func TestForwarderStopsOnContextCancel(t *testing.T) {
	fleet := &tunnelFleet{echo: udpEcho(t)}
	f, err := NewForwarder(&fakeTunnel{fleet: fleet}, ForwarderOptions{Listen: "127.0.0.1:0", Target: "162.159.192.1:2408"})
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	if _, err := echoOnce(t, f.Addr(), []byte("ping"), 3*time.Second); err != nil {
		t.Fatalf("пакет: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run после отмены: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после отмены контекста")
	}
	if s := f.Stats(); s.Sessions != 0 {
		t.Errorf("сессий после остановки = %d, ожидали 0", s.Sessions)
	}
	if err := f.Close(); err != nil {
		t.Errorf("повторный Close: %v", err)
	}
}

// Параметры форвардера проверяются до старта: адрес — только loopback, цель — ip:port.
func TestNewForwarderValidation(t *testing.T) {
	tn := &fakeTunnel{fleet: &tunnelFleet{}}
	cases := []struct {
		name string
		opts ForwarderOptions
	}{
		{"пустая цель", ForwarderOptions{Listen: "127.0.0.1:0"}},
		{"цель без порта", ForwarderOptions{Listen: "127.0.0.1:0", Target: "162.159.192.1"}},
		{"не loopback", ForwarderOptions{Listen: "0.0.0.0:0", Target: "162.159.192.1:2408"}},
		{"битый адрес", ForwarderOptions{Listen: "не-адрес", Target: "162.159.192.1:2408"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if f, err := NewForwarder(tn, c.opts); err == nil {
				_ = f.Close()
				t.Error("ожидали ошибку параметров")
			}
		})
	}
	if f, err := NewForwarder(nil, ForwarderOptions{Target: "162.159.192.1:2408"}); err == nil {
		_ = f.Close()
		t.Error("без туннеля должна быть ошибка")
	}
}
