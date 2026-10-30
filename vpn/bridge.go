package vpn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/Fatalist0001/yet_another_warp_generator/dnscheck"
)

// nicID — единственная сетевая карта моста.
const nicID = tcpip.NICID(1)

// dnsTryTimeout — предел одного обмена с одним резолвером.
//
// Он меньше общего дозвона: за это время мост успевает перебрать остальных
// кандидатов и всё равно ответить клиенту раньше, чем тот сдастся сам.
const dnsTryTimeout = 3 * time.Second

// BridgeOptions — параметры моста.
type BridgeOptions struct {
	// Device — устройство пакетов (TUN).
	Device Device
	// TUNAddr — адрес TUN (адрес стека и адрес системного DNS).
	TUNAddr netip.Addr
	// MTU — MTU устройства: предел размера пакета для стека.
	MTU int
	// Dial — дозвон через SOCKS5 текущей цепочки.
	Dial DialFunc
	// DialTimeout — таймаут SOCKS5-соединения.
	DialTimeout time.Duration
	// IdleTimeout — простой соединения и DNS-потока.
	IdleTimeout time.Duration
	// Resolver — текущий DNS-резолвер (ip:port). Используется, когда нет
	// Resolvers: одна попытка на один адрес.
	Resolver func() string
	// Resolvers — резолверы в порядке предпочтения. Мост пробует их по очереди
	// и только потом отвечает клиенту SERVFAIL, поэтому мёртвый резолвер не
	// оставляет систему без DNS до перезапуска (см. Resolver.Candidates).
	Resolvers func() []string
	// UDP — политика обработки UDP.
	UDP UDPPolicy
	// Logf — приёмник служебных сообщений.
	Logf func(format string, args ...any)
}

// BridgeStats — счётчики моста (для диагностики и тестов).
type BridgeStats struct {
	// TCPAccepted — принятые соединения; TCPActive — сколько живёт сейчас.
	TCPAccepted int64
	TCPActive   int64
	// UDPDropped — прочий UDP (без DNS), включая QUIC.
	UDPDropped int64
	// QUICDropped — подмножество UDPDropped: UDP/443 (QUIC не проксируем).
	QUICDropped int64
	// ICMPDropped / IPv6Dropped / OtherDropped / Malformed — отброшенное.
	ICMPDropped  int64
	IPv6Dropped  int64
	OtherDropped int64
	Malformed    int64
	// IPv6Rejected / UDPRejected — сколько отброшенных пакетов закрыли ошибкой
	// ICMP, чтобы приложение не ждало собственного таймаута.
	IPv6Rejected int64
	UDPRejected  int64
	// DNSFlows — сколько DNS-потоков клиентов обслужили, DNSErrors — сбои обмена.
	DNSFlows  int64
	DNSErrors int64
	// ICMPThrottled — отказы, которые не отправлены из-за предела темпа.
	ICMPThrottled int64
}

// Bridge — мост между TUN и SOCKS5: принимает пакеты к любым адресам, поднимает
// TCP-потоки и перехватывает DNS. Стек gVisor работает в режимах promiscuous и
// spoofing, поэтому ему не нужен адрес назначения на интерфейсе: TCP-форвардер
// отвечает на любой SYN, UDP-форвардер принимает датаграммы к любому адресу.
type Bridge struct {
	opts BridgeOptions

	stack  *stack.Stack
	ep     *channel.Endpoint
	tcpFwd *tcp.Forwarder
	udpFwd *udp.Forwarder
	dev    Device

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	stats  BridgeStats
	closed bool

	// writeMu сериализует записи в устройство: насос стека и ответы ICMP
	// пишут из разных горутин, а устройство этого не допускает (Wintun требует
	// одну сессию записи, а nativeDevice переиспользует буфер под заголовок).
	writeMu sync.Mutex

	// Лимит темпа ICMP-отказов: приложение с мёртвыми соединениями шлёт пакеты
	// пачками, и поток ошибок не должен занимать насос устройства.
	rejectMu   sync.Mutex
	rejectSec  int64
	rejectNums int
}

// NewBridge собирает стек и форвардеры. Пакеты не идут, пока не вызван Start.
func NewBridge(opts BridgeOptions) (*Bridge, error) {
	if opts.Device == nil {
		return nil, errors.New("vpn: не задано устройство пакетов")
	}
	if opts.Dial == nil {
		return nil, errors.New("vpn: не задан дозвон через SOCKS5")
	}
	if !opts.TUNAddr.Is4() {
		return nil, fmt.Errorf("vpn: адрес TUN %q должен быть IPv4-адресом", opts.TUNAddr)
	}
	if opts.MTU <= 0 {
		opts.MTU = DefaultTUNMTU
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	policy, err := ParseUDPPolicy(string(opts.UDP))
	if err != nil {
		return nil, err
	}
	opts.UDP = policy

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		// Локальную доставку выключаем: пакеты к нашим же адресам должны уходить
		// в туннель, а не разбираться стеком.
		HandleLocal: false,
	})
	ep := channel.New(1024, uint32(opts.MTU), "")
	if err := s.CreateNIC(nicID, ep); err != nil {
		s.Close()
		return nil, fmt.Errorf("vpn: сетевой интерфейс моста: %v", err)
	}
	// Принимаем пакеты к любым адресам (promiscuous) и разрешаем отвечать с них
	// (spoofing): мост не адресуется сам, он проксирует чужой трафик.
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("vpn: promiscuous-режим: %v", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("vpn: spoofing-режим: %v", err)
	}
	proto := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(opts.TUNAddr.As4()).WithPrefix(),
	}
	if err := s.AddProtocolAddress(nicID, proto, stack.AddressProperties{}); err != nil {
		s.Close()
		return nil, fmt.Errorf("vpn: адрес моста: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	b := &Bridge{opts: opts, stack: s, ep: ep, dev: opts.Device}

	b.tcpFwd = tcp.NewForwarder(s, 0, maxInFlight, b.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, b.tcpFwd.HandlePacket)
	b.udpFwd = udp.NewForwarder(s, b.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, b.udpFwd.HandlePacket)

	return b, nil
}

// Start запускает насосы пакетов: устройство → стек и стек → устройство.
// Возвращается сразу; всё останавливается Close.
func (b *Bridge) Start(ctx context.Context) {
	b.mu.Lock()
	if b.closed || b.cancel != nil {
		b.mu.Unlock()
		return
	}
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(2)
	b.mu.Unlock()

	go b.pumpDevice()
	go b.pumpStack()
}

// Stats — снимок счётчиков.
func (b *Bridge) Stats() BridgeStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Close останавливает насосы, закрывает стек и устройство. Повторный вызов
// безопасен.
func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cancel := b.cancel
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Форвардеры отдельного закрытия не требуют: их ресурсы живут в стеке, а
	// обработчики транспорта менять на работающем стеке нельзя.
	// Закрытие канала будит насос стека, закрытие устройства — насос устройства.
	b.ep.Close()
	if b.stack != nil {
		b.stack.Close()
		b.stack.Wait()
	}
	var err error
	if b.dev != nil {
		err = b.dev.Close()
	}
	b.wg.Wait()

	stats := b.Stats()
	b.log("мост остановлен: соединений %d, отброшено UDP %d (QUIC %d), ICMP %d, IPv6 %d, отказов ICMP %d (IPv6 %d, UDP %d, сверх лимита %d), DNS-потоков %d (ошибок %d)",
		stats.TCPAccepted, stats.UDPDropped, stats.QUICDropped, stats.ICMPDropped, stats.IPv6Dropped,
		stats.IPv6Rejected+stats.UDPRejected, stats.IPv6Rejected, stats.UDPRejected, stats.ICMPThrottled,
		stats.DNSFlows, stats.DNSErrors)
	return err
}

// writeDevice — единственная точка записи пакетов в устройство.
func (b *Bridge) writeDevice(data []byte) (int, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.dev.Write(data)
}

// allowReject ограничивает темп ICMP-отказов (не больше maxRejectPerSecond).
func (b *Bridge) allowReject() bool {
	sec := time.Now().Unix()
	b.rejectMu.Lock()
	defer b.rejectMu.Unlock()
	if b.rejectSec != sec {
		b.rejectSec, b.rejectNums = sec, 0
	}
	if b.rejectNums >= maxRejectPerSecond {
		b.bump(func(s *BridgeStats) { s.ICMPThrottled++ })
		return false
	}
	b.rejectNums++
	return true
}

// rejectUDP отвечает на непроксируемый UDP «порт недоступен»: приложение
// узнаёт об отказе сразу и откатывается на TCP.
func (b *Bridge) rejectUDP(pkt []byte) {
	reply, ok := icmpv4PortUnreachable(pkt)
	if !ok {
		return
	}
	b.bump(func(s *BridgeStats) { s.UDPRejected++ })
	b.reject(reply)
}

// reject отправляет готовый ICMP-пакет обратно в TUN. Ошибка записи не валит
// мост: отказ — подсказка приложению, а не условие работы туннеля.
func (b *Bridge) reject(pkt []byte) {
	if b.isClosed() || !b.allowReject() {
		return
	}
	if ctx := b.ctx; ctx != nil && ctx.Err() != nil {
		return
	}
	if _, err := b.writeDevice(pkt); err != nil && b.ctx.Err() == nil && !b.isClosed() {
		b.log("мост: запись ICMP-отказа: %v", err)
	}
}

// pumpDevice читает пакеты из TUN и отдаёт их политике пакетов.
//
// Как и насос стека, один сбой чтения не гасит мост: потеряется один пакет, а
// не весь системный туннель (предел на совсем неисправное устройство —
// maxPumpFails).
func (b *Bridge) pumpDevice() {
	defer b.wg.Done()

	buf := make([]byte, maxPacket)
	fails := 0
	for {
		n, err := b.dev.Read(buf)
		if err != nil {
			if b.ctx.Err() != nil || b.isClosed() {
				return
			}
			fails++
			if fails == 1 || fails%100 == 0 {
				b.log("мост: чтение TUN: %v (сбоев подряд: %d)", err, fails)
			}
			if fails >= maxPumpFails {
				b.log("мост: устройство не отдаёт пакеты — насос TUN остановлен")
				return
			}
			continue
		}
		fails = 0
		if n <= 0 {
			continue
		}
		b.inject(buf[:n])
	}
}

// pumpStack забирает пакеты из стека и пишет их в TUN.
//
// Сбой одной записи не гасит насос: иначе первая же ошибка устройства оставляет
// клиента без ответов, а причина видна только в --debug (см. maxPumpFails —
// предел на совсем неисправное устройство).
func (b *Bridge) pumpStack() {
	defer b.wg.Done()

	fails := 0
	for {
		pkt := b.ep.ReadContext(b.ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		data := view.AsSlice()
		_, err := b.writeDevice(data)
		view.Release()
		pkt.DecRef()
		if err != nil {
			if b.ctx.Err() != nil || b.isClosed() {
				return
			}
			fails++
			if fails == 1 || fails%100 == 0 {
				b.log("мост: запись в TUN: %v (сбоев подряд: %d)", err, fails)
			}
			if fails >= maxPumpFails {
				b.log("мост: устройство не принимает пакеты — насос стека остановлен")
				return
			}
			continue
		}
		fails = 0
	}
}

// inject применяет политику и передаёт пакет в стек.
func (b *Bridge) inject(pkt []byte) {
	switch classifyPacket(pkt) {
	case verdictStack:
		b.ep.InjectInbound(header.IPv4ProtocolNumber, stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(pkt),
		}))
	case verdictDropIPv6:
		b.bump(func(s *BridgeStats) { s.IPv6Dropped++ })
		// IPv6 через SOCKS5 не идёт. Вместо тишины — «нет маршрута»: приложение
		// сразу роняет попытку и переходит на IPv4, а не висит минутами.
		//
		// Одного ICMPv6 мало: его может не доставить сама ОС (фильтрация ICMP), и
		// тогда попытка соединения висит до таймаута приложения. Поэтому на
		// попытку соединения уходит ещё и TCP RST — его TCP-стек обрабатывает
		// без участия ICMP.
		replied := false
		if reply, ok := icmpv6NoRoute(pkt); ok {
			b.reject(reply)
			replied = true
		}
		if reply, ok := tcpResetv6(pkt); ok {
			b.reject(reply)
			replied = true
		}
		if replied {
			b.bump(func(s *BridgeStats) { s.IPv6Rejected++ })
		}
	case verdictDropICMP:
		b.bump(func(s *BridgeStats) { s.ICMPDropped++ })
	case verdictDropQUIC:
		// QUIC отбрасываем — приложение должно откатиться на TCP. Сказать об
		// этом сразу важнее, чем промолчать: иначе браузер ждёт QUIC-таймаут
		// и только потом пробует TCP.
		b.bump(func(s *BridgeStats) { s.UDPDropped++; s.QUICDropped++ })
		b.rejectUDP(pkt)
	case verdictDropUDP:
		b.bump(func(s *BridgeStats) { s.UDPDropped++ })
		b.rejectUDP(pkt)
	case verdictDropOther:
		b.bump(func(s *BridgeStats) { s.OtherDropped++ })
	case verdictDropMalformed:
		b.bump(func(s *BridgeStats) { s.Malformed++ })
	}
}

// verdict — приговор политики для входящего пакета.
type verdict int

const (
	// verdictStack — пакет идёт в стек: IPv4 TCP или UDP/53 (DNS).
	verdictStack verdict = iota
	verdictDropIPv6
	verdictDropICMP
	verdictDropUDP
	verdictDropQUIC
	verdictDropOther
	verdictDropMalformed
)

// classifyPacket — политика обработки входящего пакета. Работает только IPv4:
// IPv6 (черный маршрут/ICMP-отказ), ICMP (через SOCKS5 не проксируется) и UDP
// кроме DNS (точка расширения под проброс) не проводятся — на отброшенное мост
// отвечает ошибкой ICMP, чтобы приложение не ждало таймаута.
func classifyPacket(pkt []byte) verdict {
	if len(pkt) < header.IPv4MinimumSize {
		return verdictDropMalformed
	}
	switch pkt[0] >> 4 {
	case 6:
		return verdictDropIPv6
	case 4:
	default:
		return verdictDropMalformed
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < header.IPv4MinimumSize || len(pkt) < ihl {
		return verdictDropMalformed
	}
	// Не первый фрагмент: портов в нём нет, пусть стек соберёт пакет сам.
	if binary.BigEndian.Uint16(pkt[6:8])&0x1fff != 0 {
		return verdictStack
	}

	switch tcpip.TransportProtocolNumber(pkt[9]) {
	case header.TCPProtocolNumber:
		return verdictStack
	case header.ICMPv4ProtocolNumber:
		return verdictDropICMP
	case header.UDPProtocolNumber:
		if len(pkt) < ihl+8 {
			return verdictDropMalformed
		}
		switch dstPort := binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4]); {
		case dstPort == 53:
			return verdictStack
		case dstPort == 443:
			return verdictDropQUIC
		default:
			return verdictDropUDP
		}
	default:
		return verdictDropOther
	}
}

// handleTCP обрабатывает запрос соединения: поднимает соединение через SOCKS5.
func (b *Bridge) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		b.log("мост: соединение %s → %s не принято: %v", id.RemoteAddress, target(id), err)
		return
	}
	r.Complete(false)

	conn := gonet.NewTCPConn(&wq, ep)
	b.bump(func(s *BridgeStats) { s.TCPAccepted++; s.TCPActive++ })
	go b.serveTCP(conn, id)
}

// serveTCP ведёт одно TCP-соединение: DNS-потоки уходят выбранному резолверу,
// остальные — на запрошенный адрес.
func (b *Bridge) serveTCP(conn net.Conn, id stack.TransportEndpointID) {
	defer b.bump(func(s *BridgeStats) { s.TCPActive-- })
	defer conn.Close()

	dst := target(id)
	var remote net.Conn
	var err error
	if id.LocalPort == 53 {
		// DNS-поток уходит резолверу, а не на адрес из запроса: клиент
		// спрашивает адрес TUN, а резолвер выбирается цепочкой.
		remote, err = b.dialDNS()
		if err != nil {
			b.bump(func(s *BridgeStats) { s.DNSErrors++ })
			b.log("мост: DNS по TCP: %v", err)
			return
		}
	} else {
		ctx, cancel := context.WithTimeout(b.ctx, b.opts.DialTimeout)
		remote, err = b.opts.Dial(ctx, "tcp", dst)
		cancel()
		if err != nil {
			// При смене звена цепочки сюда попадают все новые соединения: клиент
			// получает закрытие сразу, но трафик не уходит напрямую (kill switch).
			b.log("мост: SOCKS5 %s: %v", dst, err)
			return
		}
	}
	defer remote.Close()

	pump(conn, remote, b.opts.IdleTimeout)
}

// handleUDP принимает UDP-датаграммы. Единственный UDP, который мы проксируем, —
// DNS; всё остальное решает политика --udp (сейчас только drop). Точка
// расширения под проброс UDP: сюда добавится создание SOCKS5 UDP ASSOCIATE.
func (b *Bridge) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	if id.LocalPort != 53 {
		b.bump(func(s *BridgeStats) { s.UDPDropped++ })
		return false
	}

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		b.log("мост: DNS-поток %s не создан: %v", target(id), err)
		return true
	}
	conn := gonet.NewUDPConn(&wq, ep)
	b.bump(func(s *BridgeStats) { s.DNSFlows++ })
	go b.serveDNS(conn)
	return true
}

// serveDNS пересылает запросы клиента как DNS-over-TCP через SOCKS5: через
// SOCKS5 доступен только TCP, а адрес назначения запроса не важен — выбор сделан
// при подборе резолвера. Кэша нет: каждый запрос уходит в сеть.
//
// Резолверов может быть несколько, и они перебираются по очереди. Молчание здесь
// дороже ошибки: если клиент не получил ни ответа, ни отказа, он ждёт свой
// таймаут на каждую попытку, а приложение в это время считает, что сети нет. Ни
// один резолвер не ответил — клиент сразу получает SERVFAIL.
func (b *Bridge) serveDNS(conn *gonet.UDPConn) {
	defer conn.Close()

	var upstream *dnsUpstream
	defer func() {
		if upstream != nil {
			upstream.close()
		}
	}()

	buf := make([]byte, dnsBuffer)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(b.opts.IdleTimeout)); err != nil {
			return
		}
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		query := append([]byte(nil), buf[:n]...)

		resp := b.exchangeDNS(&upstream, query)
		if resp == nil {
			b.bump(func(s *BridgeStats) { s.DNSErrors++ })
			resp = dnsServfail(query)
			if resp == nil {
				continue
			}
		}
		if err := conn.SetWriteDeadline(time.Now().Add(dnsTryTimeout)); err != nil {
			return
		}
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// dnsUpstream — соединение с одним резолвером, живущее вместе с DNS-потоком
// клиента: пока резолвер отвечает, он переиспользуется.
type dnsUpstream struct {
	addr string
	conn net.Conn
}

func (u *dnsUpstream) close() {
	if u == nil || u.conn == nil {
		return
	}
	_ = u.conn.Close()
	u.conn = nil
}

// exchangeDNS отправляет запрос резолверам по очереди: сбой одного — повод
// попробовать следующего, а не бросить запрос. nil — никто не ответил.
func (b *Bridge) exchangeDNS(up **dnsUpstream, query []byte) []byte {
	for _, addr := range b.dnsCandidates() {
		resp, timedOut := b.tryDNS(up, addr, query)
		if resp != nil {
			return resp
		}
		if timedOut {
			// Резолвер не отвечает — второй раз ждать его незачем.
			continue
		}
		// Соединение могло закрыться со стороны сервера: свежий дозвон до того
		// же адреса обычно решает, и только потом идём к следующему.
		if resp, _ := b.tryDNS(up, addr, query); resp != nil {
			return resp
		}
	}
	return nil
}

// tryDNS — один обмен с одним резолвером. timedOut означает, что адрес не
// ответил вовсе (а не сломал уже открытое соединение).
func (b *Bridge) tryDNS(up **dnsUpstream, addr string, query []byte) (resp []byte, timedOut bool) {
	u := *up
	if u != nil && u.addr != addr {
		u.close()
		u, *up = nil, nil
	}
	if u == nil {
		conn, err := b.dialOne(addr)
		if err != nil {
			b.log("мост: DNS через SOCKS5 (%s): %v", addr, err)
			return nil, false
		}
		u = &dnsUpstream{addr: addr, conn: conn}
		*up = u
	}
	resp, err := dnscheck.ExchangeTCP(u.conn, query, dnsTryTimeout)
	if err != nil {
		b.log("мост: обмен DNS с %s: %v", addr, err)
		u.close()
		*up = nil
		return nil, isTimeout(err)
	}
	return resp, false
}

// isTimeout — истёк срок обмена (резолвер молчит), а не оборвалось соединение.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// dialDNS — соединение с первым ответившим резолвером: для DNS по TCP, где
// поток ведёт pump, а не serveDNS.
func (b *Bridge) dialDNS() (net.Conn, error) {
	var firstErr error
	for _, addr := range b.dnsCandidates() {
		conn, err := b.dialOne(addr)
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", addr, err)
		}
	}
	if firstErr == nil {
		return nil, errors.New("не выбран ни один DNS-резолвер")
	}
	return nil, firstErr
}

// dialOne открывает соединение через SOCKS5 с одним резолвером.
func (b *Bridge) dialOne(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(b.ctx, b.opts.DialTimeout)
	defer cancel()
	return b.opts.Dial(ctx, "tcp", addr)
}

// dnsCandidates — резолверы, которые стоит попробовать.
func (b *Bridge) dnsCandidates() []string {
	if b.opts.Resolvers != nil {
		if list := b.opts.Resolvers(); len(list) > 0 {
			return list
		}
	}
	if addr := b.currentResolver(); addr != "" {
		return []string{addr}
	}
	return nil
}

// currentResolver — резолвер, выбранный после готовности цепочки.
func (b *Bridge) currentResolver() string {
	if b.opts.Resolver == nil {
		return ""
	}
	return b.opts.Resolver()
}

// target — адрес назначения потока (ip:port), который запросил клиент.
func target(id stack.TransportEndpointID) string {
	return net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))
}

func (b *Bridge) bump(fn func(*BridgeStats)) {
	b.mu.Lock()
	fn(&b.stats)
	b.mu.Unlock()
}

func (b *Bridge) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *Bridge) log(format string, args ...any) {
	if b.opts.Logf != nil {
		b.opts.Logf(format, args...)
	}
}
