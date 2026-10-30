package vpn

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
)

// --- полные IPv6/IPv4-пакеты с настоящими адресами ---

// ipv6UDP собирает минимальный IPv6-пакет с UDP-заголовком (8 байт).
func ipv6UDP(src, dst netip.Addr) []byte {
	pkt := make([]byte, ipv6HeaderLen+8)
	pkt[0] = 0x60
	binary.BigEndian.PutUint16(pkt[4:6], 8)
	pkt[6] = 17 // UDP
	pkt[7] = 64
	a := src.As16()
	copy(pkt[8:24], a[:])
	b := dst.As16()
	copy(pkt[24:40], b[:])
	return pkt
}

// ipv4UDP собирает IPv4-пакет с UDP-заголовком и портом назначения.
func ipv4UDP(src, dst netip.Addr, dstPort uint16) []byte {
	pkt := make([]byte, ipv4HeaderLen+8)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = 17
	a := src.As4()
	copy(pkt[12:16], a[:])
	b := dst.As4()
	copy(pkt[16:20], b[:])
	binary.BigEndian.PutUint16(pkt[ipv4HeaderLen+2:ipv4HeaderLen+4], dstPort)
	return pkt
}

// onesSum — независимая от icmp.go проверка контрольной суммы: сложение
// 16-битными словами без учёта уже вписанной суммы должно дать 0xffff.
func onesSum(parts ...[]byte) uint16 {
	var sum uint32
	for _, p := range parts {
		for i := 0; i+1 < len(p); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(p[i : i+2]))
		}
		if len(p)%2 == 1 {
			sum += uint32(p[len(p)-1]) << 8
		}
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return uint16(sum)
}

var (
	tunClient = netip.MustParseAddr("198.18.0.1")
	ipv6Peer  = netip.MustParseAddr("2a00:1450:4010:c05::64")
	ipv4Peer  = netip.MustParseAddr("142.250.74.100")
)

// Ответ на IPv6: ICMPv6 «нет маршрута» от имени адресата с корректной суммой.
func TestICMPv6NoRoutePacket(t *testing.T) {
	in := ipv6UDP(tunClient.Unmap(), ipv6Peer)
	reply, ok := icmpv6NoRoute(in)
	if !ok {
		t.Fatal("ответ не построен")
	}
	if reply[0]>>4 != 6 {
		t.Errorf("версия %d, ожидали 6", reply[0]>>4)
	}
	if reply[6] != icmpv6NextHeader {
		t.Errorf("следующий заголовок %d, ожидали %d", reply[6], icmpv6NextHeader)
	}
	if got := binary.BigEndian.Uint16(reply[4:6]); int(got) != len(reply)-ipv6HeaderLen {
		t.Errorf("длина полезной нагрузки %d, ожидали %d", got, len(reply)-ipv6HeaderLen)
	}
	// Источник — исходный адресат, иначе ядро не сопоставит ошибку с сокетом.
	if !bytes.Equal(reply[8:24], in[24:40]) {
		t.Error("источник ответа не равен адресату исходного пакета")
	}
	if !bytes.Equal(reply[24:40], in[8:24]) {
		t.Error("адресат ответа не равен источнику исходного пакета")
	}
	if reply[40] != icmpv6DestinationUnreachable || reply[41] != icmpv6CodeNoRoute {
		t.Errorf("тип/код %d/%d, ожидали %d/%d",
			reply[40], reply[41], icmpv6DestinationUnreachable, icmpv6CodeNoRoute)
	}
	if !bytes.Equal(reply[48:], in) {
		t.Error("исходный пакет не вложен в ошибку целиком")
	}

	pseudo := make([]byte, 40)
	copy(pseudo[0:16], reply[8:24])
	copy(pseudo[16:32], reply[24:40])
	binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(reply)-ipv6HeaderLen))
	pseudo[39] = icmpv6NextHeader
	if sum := onesSum(pseudo, reply[ipv6HeaderLen:]); sum != 0xffff {
		t.Errorf("контрольная сумма ICMPv6 неверна: 0x%04x", sum)
	}
}

// Ответ на не-DNS UDP: ICMPv4 «порт недоступен» с корректными суммами.
func TestICMPv4PortUnreachablePacket(t *testing.T) {
	in := ipv4UDP(tunClient.Unmap(), ipv4Peer, 443)
	reply, ok := icmpv4PortUnreachable(in)
	if !ok {
		t.Fatal("ответ не построен")
	}
	if reply[0]>>4 != 4 || reply[9] != icmpv4ProtocolNumber {
		t.Fatalf("не ICMPv4: версия %d, протокол %d", reply[0]>>4, reply[9])
	}
	if got := binary.BigEndian.Uint16(reply[2:4]); int(got) != len(reply) {
		t.Errorf("длина пакета %d, ожидали %d", got, len(reply))
	}
	if !bytes.Equal(reply[12:16], in[16:20]) || !bytes.Equal(reply[16:20], in[12:16]) {
		t.Error("адреса ответа не переставлены")
	}
	if reply[20] != icmpv4DestinationUnreachable || reply[21] != icmpv4CodePortUnreachable {
		t.Errorf("тип/код %d/%d, ожидали %d/%d",
			reply[20], reply[21], icmpv4DestinationUnreachable, icmpv4CodePortUnreachable)
	}
	if !bytes.Equal(reply[28:], in) {
		t.Error("исходный пакет не вложен в ошибку целиком")
	}
	if sum := onesSum(reply[:ipv4HeaderLen]); sum != 0xffff {
		t.Errorf("контрольная сумма IPv4 неверна: 0x%04x", sum)
	}
	if sum := onesSum(reply[ipv4HeaderLen:]); sum != 0xffff {
		t.Errorf("контрольная сумма ICMPv4 неверна: 0x%04x", sum)
	}
}

// Ответ не строится там, где он бессмысленен или опасен (переписка ошибок).
func TestICMPRejectSkipsServicePackets(t *testing.T) {
	icmpv6 := ipv6UDP(tunClient.Unmap(), ipv6Peer)
	icmpv6[6] = icmpv6NextHeader
	if _, ok := icmpv6NoRoute(icmpv6); ok {
		t.Error("ответ на ICMPv6: получилась бы переписка ошибок")
	}

	dad := ipv6UDP(netip.IPv6Unspecified(), netip.MustParseAddr("ff02::1:ff00:1"))
	if _, ok := icmpv6NoRoute(dad); ok {
		t.Error("ответ на пакет с неопределённым источником")
	}

	multicast := ipv6UDP(netip.MustParseAddr("ff02::1"), ipv6Peer)
	if _, ok := icmpv6NoRoute(multicast); ok {
		t.Error("ответ на многоадресный источник")
	}

	linkLocal := ipv6UDP(netip.MustParseAddr("fe80::1"), ipv6Peer)
	if _, ok := icmpv6NoRoute(linkLocal); ok {
		t.Error("ответ на link-local источник (связь с соседями)")
	}

	icmpv4 := ipv4UDP(tunClient.Unmap(), ipv4Peer, 0)
	icmpv4[9] = icmpv4ProtocolNumber
	if _, ok := icmpv4PortUnreachable(icmpv4); ok {
		t.Error("ответ на ICMPv4: получилась бы переписка ошибок")
	}

	unassigned := ipv4UDP(netip.IPv4Unspecified(), ipv4Peer, 443)
	if _, ok := icmpv4PortUnreachable(unassigned); ok {
		t.Error("ответ на пакет с адресом 0.0.0.0")
	}

	if _, ok := icmpv6NoRoute([]byte{0x60, 0x00}); ok {
		t.Error("ответ на обрезанный IPv6")
	}
	if _, ok := icmpv4PortUnreachable([]byte{0x45, 0x00}); ok {
		t.Error("ответ на обрезанный IPv4")
	}
}

// Вложенный пакет обрезается по MTU, а не копируется целиком.
func TestICMPRejectCapsQuote(t *testing.T) {
	big6 := make([]byte, 4000)
	copy(big6, ipv6UDP(tunClient.Unmap(), ipv6Peer))
	binary.BigEndian.PutUint16(big6[4:6], uint16(len(big6)-ipv6HeaderLen))
	reply6, ok := icmpv6NoRoute(big6)
	if !ok {
		t.Fatal("ответ IPv6 не построен")
	}
	if len(reply6) > icmpv6MaxPacket {
		t.Errorf("ответ IPv6 %d байт, предел %d", len(reply6), icmpv6MaxPacket)
	}

	big4 := make([]byte, 4000)
	copy(big4, ipv4UDP(tunClient.Unmap(), ipv4Peer, 443))
	binary.BigEndian.PutUint16(big4[2:4], uint16(len(big4)))
	reply4, ok := icmpv4PortUnreachable(big4)
	if !ok {
		t.Fatal("ответ IPv4 не построен")
	}
	if len(reply4) > icmpv4MaxPacket {
		t.Errorf("ответ IPv4 %d байт, предел %d", len(reply4), icmpv4MaxPacket)
	}
}

// --- мост отвечает на отброшенное ---

// recordDevice — устройство, которое только запоминает записанные пакеты.
type recordDevice struct {
	writes [][]byte
}

func (d *recordDevice) Read([]byte) (int, error) {
	return 0, errors.New("в тесте не читается")
}
func (d *recordDevice) Write(p []byte) (int, error) {
	d.writes = append(d.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (d *recordDevice) Name() string { return "test0" }
func (d *recordDevice) MTU() int     { return 1400 }
func (d *recordDevice) Close() error { return nil }

// Отброшенные IPv6 и не-DNS UDP закрываются ошибкой ICMP, а не молчанием.
func TestBridgeRejectsDroppedPackets(t *testing.T) {
	dev := &recordDevice{}
	bridge, err := NewBridge(BridgeOptions{
		Device:  dev,
		TUNAddr: tunClient,
		MTU:     1400,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("не нужен")
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	bridge.inject(ipv6UDP(tunClient.Unmap(), ipv6Peer))
	bridge.inject(ipv4UDP(tunClient.Unmap(), ipv4Peer, 443))
	bridge.inject(ipv4UDP(tunClient.Unmap(), ipv4Peer, 3478))

	if len(dev.writes) != 3 {
		t.Fatalf("записано %d пакетов, ожидали 3", len(dev.writes))
	}
	if dev.writes[0][0]>>4 != 6 || dev.writes[0][6] != icmpv6NextHeader {
		t.Errorf("первый ответ не ICMPv6: % x", dev.writes[0][:8])
	}
	for i, w := range dev.writes[1:] {
		if w[0]>>4 != 4 || w[9] != icmpv4ProtocolNumber || w[20] != icmpv4DestinationUnreachable {
			t.Errorf("ответ %d не ICMPv4 unreachable: % x", i+1, w[:24])
		}
	}

	stats := bridge.Stats()
	if stats.IPv6Rejected != 1 || stats.UDPRejected != 2 {
		t.Errorf("отказов IPv6 %d, UDP %d, ожидали 1 и 2", stats.IPv6Rejected, stats.UDPRejected)
	}
	if stats.IPv6Dropped != 1 || stats.UDPDropped != 2 || stats.QUICDropped != 1 {
		t.Errorf("счётчики отброшенного: IPv6 %d, UDP %d, QUIC %d",
			stats.IPv6Dropped, stats.UDPDropped, stats.QUICDropped)
	}
}

// Предел темпа не даёт мёртвым соединениям занять насос.
func TestBridgeRejectRateLimited(t *testing.T) {
	dev := &recordDevice{}
	bridge, err := NewBridge(BridgeOptions{
		Device:  dev,
		TUNAddr: tunClient,
		MTU:     1400,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("не нужен")
		},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	pkt := ipv4UDP(tunClient.Unmap(), ipv4Peer, 443)
	for i := 0; i < maxRejectPerSecond+50; i++ {
		bridge.inject(pkt)
	}
	if len(dev.writes) > maxRejectPerSecond {
		t.Errorf("отправлено %d отказов, предел %d", len(dev.writes), maxRejectPerSecond)
	}
	if bridge.Stats().ICMPThrottled == 0 {
		t.Error("превышение лимита не зафиксировано")
	}
}
