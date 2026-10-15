package vpn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// --- устройство и клиентский стек ---

// testDevice — фейковое TunDevice: пакеты ходят между мостом и стеком клиента,
// поэтому мост проверяется целиком — без TUN, root и сети. Это вторая половина
// схемы «два стека, соединённые фейковым устройством»: один стек в мосте, второй
// здесь.
type testDevice struct {
	ep     *channel.Endpoint
	name   string
	mtu    int
	ctx    context.Context
	cancel context.CancelFunc
}

func (d *testDevice) Read(p []byte) (int, error) {
	pkt := d.ep.ReadContext(d.ctx)
	if pkt == nil {
		return 0, errors.New("фейковое устройство закрыто")
	}
	view := pkt.ToView()
	n := copy(p, view.AsSlice())
	pkt.DecRef()
	return n, nil
}

func (d *testDevice) Write(p []byte) (int, error) {
	d.ep.InjectInbound(header.IPv4ProtocolNumber, stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), p...)),
	}))
	return len(p), nil
}

func (d *testDevice) Name() string { return d.name }
func (d *testDevice) MTU() int     { return d.mtu }
func (d *testDevice) Close() error {
	d.cancel()
	d.ep.Close()
	return nil
}

const testMTU = 1400

var (
	clientAddr = netip.MustParseAddr("10.0.0.2")
	tunAddr    = netip.MustParseAddr("198.18.0.1")
	// remoteAddr — произвольный адрес назначения: мост не адресуется сам.
	remoteAddr = netip.MustParseAddr("10.20.30.40")
)

// testClient — стек клиента и устройство, которым он подключён к мосту.
type testClient struct {
	stack *stack.Stack
	dev   *testDevice
	once  sync.Once
}

// close останавливает стек клиента и устройство (безопасно вызывать дважды).
func (c *testClient) close() {
	c.once.Do(func() {
		c.stack.Close()
		c.stack.Wait()
		_ = c.dev.Close()
	})
}

func newTestClient(t *testing.T) *testClient {
	t.Helper()
	ep := channel.New(1024, uint32(testMTU), "")
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if err := s.CreateNIC(1, ep); err != nil {
		t.Fatalf("клиентский интерфейс: %v", err)
	}
	proto := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(clientAddr.As4()).WithPrefix(),
	}
	if err := s.AddProtocolAddress(1, proto, stack.AddressProperties{}); err != nil {
		t.Fatalf("адрес клиента: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})

	ctx, cancel := context.WithCancel(context.Background())
	dev := &testDevice{ep: ep, name: "test0", mtu: testMTU, ctx: ctx, cancel: cancel}
	client := &testClient{stack: s, dev: dev}
	t.Cleanup(client.close)
	return client
}

// --- поддельный SOCKS5 ---

// fakeSOCKS5 — поддельный SOCKS5-прокси на loopback: принимает CONNECT, соединяет
// с локальным бэкендом и запоминает запрошенные адреса. Никакого выхода в сеть.
type fakeSOCKS5 struct {
	ln      net.Listener
	backend func(target string) (net.Conn, error)

	mu      sync.Mutex
	targets []string
	refused []string
}

func newFakeSOCKS5(t *testing.T, backend func(target string) (net.Conn, error)) *fakeSOCKS5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("поддельный SOCKS5: %v", err)
	}
	s := &fakeSOCKS5{ln: ln, backend: backend}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s
}

func (s *fakeSOCKS5) addr() string { return s.ln.Addr().String() }

func (s *fakeSOCKS5) handle(c net.Conn) {
	defer c.Close()
	if err := socksHandshake(c, s); err != nil {
		return
	}
}

// socksHandshake разбирает приветствие и CONNECT, отвечает и проксирует байты.
func socksHandshake(c net.Conn, s *fakeSOCKS5) error {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return err
	}
	if head[0] != 5 {
		return fmt.Errorf("версия SOCKS %d", head[0])
	}
	if _, err := io.ReadFull(c, make([]byte, head[1])); err != nil {
		return err
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // без аутентификации
		return err
	}

	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return err
	}
	if req[0] != 5 || req[1] != 1 { // CONNECT
		return fmt.Errorf("команда SOCKS %d", req[1])
	}
	host, err := socksHost(c, req[3])
	if err != nil {
		return err
	}
	var portb [2]byte
	if _, err := io.ReadFull(c, portb[:]); err != nil {
		return err
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portb[:]))))

	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()

	backend, err := s.backend(target)
	if err != nil {
		s.mu.Lock()
		s.refused = append(s.refused, target)
		s.mu.Unlock()
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}) // connection refused
		return err
	}
	defer backend.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, c); halfClose(backend); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, backend); halfClose(c); done <- struct{}{} }()
	<-done
	<-done
	return nil
}

func socksHost(c net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", err
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return "", err
		}
		return string(name), nil
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	}
	return "", fmt.Errorf("неизвестный тип адреса %d", atyp)
}

func (s *fakeSOCKS5) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

// refused — адреса, к которым поддельный прокси не смог подключиться.
func (s *fakeSOCKS5) refusedTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refused...)
}

// --- локальные бэкенды ---

// tcpEcho поднимает эхо-сервер на loopback: возвращает всё, что получил, и
// закрывает сторону записи на EOF.
func tcpEcho(t *testing.T) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("эхо-сервер: %v", err)
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
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						// Клиент закрыл запись — отвечаем тем же.
						halfClose(c)
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// dnsOverTCP поднимает поддельный DNS-over-TCP на loopback. delay — задержка
// ответа (для проверки выбора резолвера).
func dnsOverTCP(t *testing.T, delay time.Duration) (addr string, queries *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("DNS-сервер: %v", err)
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
				for {
					var head [2]byte
					if _, err := io.ReadFull(c, head[:]); err != nil {
						return
					}
					query := make([]byte, int(binary.BigEndian.Uint16(head[:])))
					if _, err := io.ReadFull(c, query); err != nil {
						return
					}
					count.Add(1)
					if delay > 0 {
						time.Sleep(delay)
					}
					var msg dnsmessage.Message
					if err := msg.Unpack(query); err != nil {
						return
					}
					resp := dnsAnswer(msg.Header.ID)
					frame := make([]byte, 2+len(resp))
					binary.BigEndian.PutUint16(frame, uint16(len(resp)))
					copy(frame[2:], resp)
					if _, err := c.Write(frame); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), &count
}

// dnsAnswer собирает ответ A для dnscheck.QueryName.
func dnsAnswer(id uint16) []byte {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName("cloudflare.com."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName("cloudflare.com."),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   60,
			},
			Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 7}},
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		panic(err)
	}
	return packed
}

// --- сборка стенда ---

// bridgeStand — мост, клиентский стек и поддельный SOCKS5 для одного теста.
type bridgeStand struct {
	bridge   *Bridge
	client   *testClient
	socks    *fakeSOCKS5
	resolver string
}

func newBridgeStand(t *testing.T, backend func(target string) (net.Conn, error)) *bridgeStand {
	t.Helper()
	socks := newFakeSOCKS5(t, backend)
	client := newTestClient(t)
	resolver := "1.1.1.1:53"

	bridge, err := NewBridge(BridgeOptions{
		Device:  client.dev,
		TUNAddr: tunAddr,
		MTU:     testMTU,
		// Дозвон идёт через поддельный SOCKS5: мост не знает, что за ним не сеть.
		Dial: DialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialSocks5(ctx, socks.addr(), address, nil)
		}),
		DialTimeout: 3 * time.Second,
		IdleTimeout: 5 * time.Second,
		Resolver:    func() string { return resolver },
		UDP:         UDPDrop,
		Logf:        t.Logf,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bridge.Start(ctx)
	t.Cleanup(func() {
		cancel()
		if err := bridge.Close(); err != nil {
			t.Logf("Close: %v", err)
		}
	})
	return &bridgeStand{bridge: bridge, client: client, socks: socks, resolver: resolver}
}

// dialTCPFrom — соединение из клиентского стека на произвольный адрес.
func dialTCPFrom(ctx context.Context, c *testClient, addr netip.AddrPort) (net.Conn, error) {
	return gonet.DialContextTCP(ctx, c.stack, tcpip.FullAddress{
		Addr: tcpip.AddrFrom4(addr.Addr().As4()),
		Port: addr.Port(),
	}, ipv4.ProtocolNumber)
}

// dialTCP — то же для стенда.
func (s *bridgeStand) dialTCP(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	return dialTCPFrom(ctx, s.client, addr)
}

// dialUDP — UDP-сокет из клиентского стека на произвольный адрес.
func (s *bridgeStand) dialUDP(addr netip.AddrPort) (*gonet.UDPConn, error) {
	full := tcpip.FullAddress{Addr: tcpip.AddrFrom4(addr.Addr().As4()), Port: addr.Port()}
	return gonet.DialUDP(s.client.stack, nil, &full, ipv4.ProtocolNumber)
}

// --- тесты ---

// Данные идут в обе стороны через мост и поддельный SOCKS5.
func TestBridgeTCPRelay(t *testing.T) {
	echo := tcpEcho(t)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	dst := netip.AddrPortFrom(remoteAddr, 80)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := stand.dialTCP(ctx, dst)
	if err != nil {
		t.Fatalf("клиент не подключился: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("привет")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(buf[:n]) != "привет" {
		t.Errorf("вернулось %q", buf[:n])
	}

	// Мост запросил у SOCKS5 адрес, который назвал клиент.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := stand.socks.requested()
		if len(got) == 1 && got[0] == dst.String() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SOCKS5 запросил %v, ожидали %s", got, dst)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stats := stand.bridge.Stats(); stats.TCPAccepted != 1 {
		t.Errorf("принятых соединений %d, ожидали 1", stats.TCPAccepted)
	}
}

// Полузакрытие: клиент закрывает запись — эхо всё равно доходит до конца.
func TestBridgeTCPHalfClose(t *testing.T) {
	echo := tcpEcho(t)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(remoteAddr, 443))
	if err != nil {
		t.Fatalf("клиент не подключился: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("хвост")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("чтение до EOF: %v", err)
	}
	if string(data) != "хвост" {
		t.Errorf("вернулось %q", data)
	}
}

// Несколько соединений параллельно — каждое получает свой ответ.
func TestBridgeTCPParallel(t *testing.T) {
	echo := tcpEcho(t)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	const parallel = 4
	var wg sync.WaitGroup
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(remoteAddr, uint16(8000+i)))
			if err != nil {
				errs <- fmt.Errorf("соединение %d: %w", i, err)
				return
			}
			defer conn.Close()

			want := fmt.Sprintf("ответ-%d", i)
			if _, err := conn.Write([]byte(want)); err != nil {
				errs <- fmt.Errorf("запись %d: %w", i, err)
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			if err != nil {
				errs <- fmt.Errorf("чтение %d: %w", i, err)
				return
			}
			if string(buf[:n]) != want {
				errs <- fmt.Errorf("соединение %d вернуло %q", i, buf[:n])
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if stats := stand.bridge.Stats(); stats.TCPAccepted != parallel {
		t.Errorf("принятых соединений %d, ожидали %d", stats.TCPAccepted, parallel)
	}
}

// Сбой SOCKS5 (смена звена цепочки) рвёт соединение, но не вешает мост.
func TestBridgeDialFailureClosesConnection(t *testing.T) {
	stand := newBridgeStand(t, func(string) (net.Conn, error) {
		return nil, errors.New("цепочка недоступна")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(remoteAddr, 80))
	if err != nil {
		t.Fatalf("хендшейк с клиентом: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 8)); err == nil {
		t.Error("ожидали закрытие соединения при сбое SOCKS5")
	}
	refused := stand.socks.refusedTargets()
	if len(refused) != 1 || refused[0] != "10.20.30.40:80" {
		t.Errorf("прокси отказал по адресам %v, ожидали 10.20.30.40:80", refused)
	}
}

// DNS: UDP-запрос клиента уходит выбранному резолверу как DNS-over-TCP.
func TestBridgeDNSOverUDP(t *testing.T) {
	dnsAddr, queries := dnsOverTCP(t, 0)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", dnsAddr) })

	conn, err := stand.dialUDP(netip.AddrPortFrom(netip.MustParseAddr("8.8.4.4"), 53))
	if err != nil {
		t.Fatalf("UDP-сокет: %v", err)
	}
	defer conn.Close()

	query := dnsQuery(0x1234)
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("запрос: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("ответ: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(buf[:n]); err != nil {
		t.Fatalf("ответ не разбирается: %v", err)
	}
	if msg.Header.ID != 0x1234 || len(msg.Answers) != 1 {
		t.Errorf("ответ = %+v", msg.Header)
	}
	if got := stand.socks.requested(); len(got) != 1 || got[0] != stand.resolver {
		t.Errorf("SOCKS5 запросил %v, ожидали резолвер %s (а не адрес из запроса)", got, stand.resolver)
	}
	if got := queries.Load(); got != 1 {
		t.Errorf("до резолвера дошло %d запросов, ожидали 1", got)
	}
}

// DNS поверх TCP: поток клиента на 53-й порт тоже уходит резолверу.
func TestBridgeDNSOverTCP(t *testing.T) {
	dnsAddr, _ := dnsOverTCP(t, 0)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", dnsAddr) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(netip.MustParseAddr("9.9.9.9"), 53))
	if err != nil {
		t.Fatalf("клиент не подключился: %v", err)
	}
	defer conn.Close()

	query := dnsQuery(0x4321)
	frame := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(frame, uint16(len(query)))
	copy(frame[2:], query)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("запрос: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		t.Fatalf("чтение длины ответа: %v", err)
	}
	resp := make([]byte, int(binary.BigEndian.Uint16(head[:])))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("чтение ответа: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(resp); err != nil {
		t.Fatalf("ответ не разбирается: %v", err)
	}
	if msg.Header.ID != 0x4321 {
		t.Errorf("id ответа = %d", msg.Header.ID)
	}
	if got := stand.socks.requested(); len(got) != 1 || got[0] != stand.resolver {
		t.Errorf("SOCKS5 запросил %v, ожидали %s", got, stand.resolver)
	}
}

// dnsQuery — DNS-запрос A на имя из dnscheck.QueryName.
func dnsQuery(id uint16) []byte {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName("cloudflare.com."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		panic(err)
	}
	return packed
}

// Политика пакетов: IPv6, ICMP, QUIC и прочий UDP отбрасываются со счётчиками,
// TCP и DNS проходят.
func TestBridgePacketPolicy(t *testing.T) {
	t.Run("классификация", func(t *testing.T) {
		cases := []struct {
			name string
			pkt  []byte
			want verdict
		}{
			{"TCP", ipv4Packet(header.TCPProtocolNumber, 80, 20), verdictStack},
			{"DNS UDP", ipv4Packet(header.UDPProtocolNumber, 53, 8), verdictStack},
			{"QUIC", ipv4Packet(header.UDPProtocolNumber, 443, 8), verdictDropQUIC},
			{"прочий UDP", ipv4Packet(header.UDPProtocolNumber, 1234, 8), verdictDropUDP},
			{"ICMP", ipv4Packet(header.ICMPv4ProtocolNumber, 0, 4), verdictDropICMP},
			{"ESP", ipv4Packet(50, 0, 4), verdictDropOther},
			{"IPv6", ipv6Packet(header.UDPProtocolNumber), verdictDropIPv6},
			{"обрезанный", []byte{0x45, 0x00}, verdictDropMalformed},
			{"не IP", []byte{0x00, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}, verdictDropMalformed},
			{"не первый фрагмент", fragment(0x2000), verdictStack},
		}
		for _, c := range cases {
			if got := classifyPacket(c.pkt); got != c.want {
				t.Errorf("%s: приговор %d, ожидали %d", c.name, got, c.want)
			}
		}
	})

	t.Run("счётчики", func(t *testing.T) {
		stand := newBridgeStand(t, func(string) (net.Conn, error) { return nil, errors.New("не нужен") })
		stand.bridge.inject(ipv6Packet(header.UDPProtocolNumber))
		stand.bridge.inject(ipv4Packet(header.ICMPv4ProtocolNumber, 0, 4))
		stand.bridge.inject(ipv4Packet(header.UDPProtocolNumber, 443, 8))
		stand.bridge.inject(ipv4Packet(header.UDPProtocolNumber, 3478, 8))
		stand.bridge.inject(ipv4Packet(50, 0, 4))
		stand.bridge.inject([]byte{0x45})

		stats := stand.bridge.Stats()
		if stats.IPv6Dropped != 1 {
			t.Errorf("IPv6 отброшено %d, ожидали 1", stats.IPv6Dropped)
		}
		if stats.ICMPDropped != 1 {
			t.Errorf("ICMP отброшено %d, ожидали 1", stats.ICMPDropped)
		}
		if stats.UDPDropped != 2 || stats.QUICDropped != 1 {
			t.Errorf("UDP отброшено %d (QUIC %d), ожидали 2 (1)", stats.UDPDropped, stats.QUICDropped)
		}
		if stats.OtherDropped != 1 || stats.Malformed != 1 {
			t.Errorf("прочее %d, битых %d, ожидали по 1", stats.OtherDropped, stats.Malformed)
		}
	})
}

// ipv4Packet собирает IPv4-пакет с нужным протоколом и портом назначения.
func ipv4Packet(proto tcpip.TransportProtocolNumber, dstPort uint16, payload int) []byte {
	pkt := make([]byte, header.IPv4MinimumSize+payload)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64 // TTL
	pkt[9] = byte(proto)
	if payload >= 8 && (proto == header.UDPProtocolNumber) {
		binary.BigEndian.PutUint16(pkt[header.IPv4MinimumSize+2:header.IPv4MinimumSize+4], dstPort)
	}
	return pkt
}

// fragment собирает пакет с ненулевым смещением фрагмента.
func fragment(offset uint16) []byte {
	pkt := ipv4Packet(header.UDPProtocolNumber, 53, 8)
	binary.BigEndian.PutUint16(pkt[6:8], offset)
	return pkt
}

// ipv6Packet собирает минимальный IPv6-пакет.
func ipv6Packet(next tcpip.TransportProtocolNumber) []byte {
	pkt := make([]byte, header.IPv6MinimumSize)
	pkt[0] = 0x60
	binary.BigEndian.PutUint16(pkt[4:6], 8)
	pkt[6] = byte(next)
	pkt[7] = 64
	return pkt
}

// Мост не оставляет горутин после остановки.
func TestBridgeLeavesNoGoroutines(t *testing.T) {
	base := runtime.NumGoroutine()

	echo := tcpEcho(t)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(remoteAddr, 80))
	if err != nil {
		t.Fatalf("клиент не подключился: %v", err)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	_ = conn.Close()

	if err := stand.bridge.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Стек клиента закрываем сами: иначе его горутины честно живут до Cleanup.
	stand.client.close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.Gosched()
		if n := runtime.NumGoroutine(); n <= base+2 {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("после остановки моста осталось %d горутин (было %d):\n%s", runtime.NumGoroutine(), base, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Счётчики TCP не растут после закрытия соединения.
func TestBridgeCountsClosedConnections(t *testing.T) {
	echo := tcpEcho(t)
	stand := newBridgeStand(t, func(string) (net.Conn, error) { return net.Dial("tcp", echo) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := stand.dialTCP(ctx, netip.AddrPortFrom(remoteAddr, 80))
	if err != nil {
		t.Fatalf("клиент не подключился: %v", err)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("запись: %v", err)
	}
	_ = conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if stats := stand.bridge.Stats(); stats.TCPActive == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("соединение не закрылось: %+v", stand.bridge.Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
