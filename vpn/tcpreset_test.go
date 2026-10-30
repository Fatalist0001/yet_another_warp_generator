package vpn

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// ipv6SYN собирает IPv6-пакет с попыткой соединения (SYN без ACK).
func ipv6SYN(src, dst string, sport, dport uint16, seq uint32) []byte {
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	pkt := make([]byte, ipv6HeaderLen+tcpHeaderLen)
	pkt[0] = 0x60
	binary.BigEndian.PutUint16(pkt[4:6], tcpHeaderLen)
	pkt[6] = tcpNextHeader
	pkt[7] = 64
	copy(pkt[8:24], s[:])
	copy(pkt[24:40], d[:])

	seg := pkt[ipv6HeaderLen:]
	binary.BigEndian.PutUint16(seg[0:2], sport)
	binary.BigEndian.PutUint16(seg[2:4], dport)
	binary.BigEndian.PutUint32(seg[4:8], seq)
	seg[12] = 5 << 4
	seg[13] = tcpFlagSYN
	binary.BigEndian.PutUint16(seg[14:16], 65535)
	binary.BigEndian.PutUint16(seg[16:18], tcpv6Checksum(s[:], d[:], seg))
	return pkt
}

// На попытку IPv6-соединения мост отвечает RST|ACK: приложение падает сразу и
// уходит на IPv4, не дожидаясь своего таймаута.
func TestTCPResetv6OnSYN(t *testing.T) {
	const (
		client = "fd00:198:18::1"
		server = "2606:4700:4700::1111"
	)
	pkt := ipv6SYN(client, server, 50123, 443, 0x11223344)

	out, ok := tcpResetv6(pkt)
	if !ok {
		t.Fatal("на IPv6-SYN не построен RST")
	}
	if len(out) != ipv6HeaderLen+tcpHeaderLen {
		t.Fatalf("длина RST = %d байт", len(out))
	}
	if out[0]>>4 != 6 || out[6] != tcpNextHeader {
		t.Errorf("заголовок IPv6: версия %d, next header %d", out[0]>>4, out[6])
	}
	if got := netip.AddrFrom16([16]byte(out[8:24])); got != netip.MustParseAddr(server) {
		t.Errorf("источник RST = %s, ожидали адрес сервера", got)
	}
	if got := netip.AddrFrom16([16]byte(out[24:40])); got != netip.MustParseAddr(client) {
		t.Errorf("адресат RST = %s, ожидали адрес клиента", got)
	}

	seg := out[ipv6HeaderLen:]
	if got := binary.BigEndian.Uint16(seg[0:2]); got != 443 {
		t.Errorf("порт источника = %d, ожидали 443", got)
	}
	if got := binary.BigEndian.Uint16(seg[2:4]); got != 50123 {
		t.Errorf("порт адресата = %d, ожидали 50123", got)
	}
	if got := binary.BigEndian.Uint32(seg[4:8]); got != 0 {
		t.Errorf("seq = %d, ожидали 0", got)
	}
	if got := binary.BigEndian.Uint32(seg[8:12]); got != 0x11223345 {
		t.Errorf("ack = %#x, ожидали seq+1", got)
	}
	if seg[12]>>4 != 5 {
		t.Errorf("длина заголовка TCP = %d слов", seg[12]>>4)
	}
	if seg[13]&tcpFlagRST == 0 || seg[13]&tcpFlagACK == 0 || seg[13]&tcpFlagSYN != 0 {
		t.Errorf("флаги TCP = %#x, ожидали RST|ACK", seg[13])
	}

	// Контрольную сумму сверяем чужой реализацией (gVisor), а не тем же хелпером.
	tcpHdr := header.TCP(seg)
	src16, dst16 := tcpip.AddrFrom16([16]byte(out[8:24])), tcpip.AddrFrom16([16]byte(out[24:40]))
	if !tcpHdr.IsChecksumValid(src16, dst16, 0, 0) {
		t.Error("контрольная сумма TCP не прошла проверку gVisor")
	}
}

// Отвечаем только на попытку соединения и только на «чистый» IPv6-TCP.
func TestTCPResetv6IgnoresOthers(t *testing.T) {
	const (
		client = "fd00:198:18::1"
		server = "2606:4700:4700::1111"
	)
	withFlags := func(flags byte) []byte {
		pkt := ipv6SYN(client, server, 50000, 443, 1)
		pkt[ipv6HeaderLen+13] = flags
		return pkt
	}
	cases := []struct {
		name string
		pkt  []byte
	}{
		{"SYN с ACK — это уже ответ", withFlags(tcpFlagSYN | tcpFlagACK)},
		{"данные без SYN", withFlags(tcpFlagACK)},
		{"пустые флаги", withFlags(0)},
		{"link-local источник", ipv6SYN("fe80::1", server, 50000, 443, 1)},
		{"многоадресный адресат", ipv6SYN(client, "ff02::1", 50000, 443, 1)},
		{"IPv4-пакет", ipv4Packet(6, 443, tcpHeaderLen)},
		{"обрезанный заголовок", []byte{0x60, 0, 0, 0, 0, 0, tcpNextHeader, 64}},
	}
	ext := ipv6SYN(client, server, 50000, 443, 1)
	ext[6] = 0 // hop-by-hop: расширенный заголовок, а не TCP
	cases = append(cases, struct {
		name string
		pkt  []byte
	}{"расширенный заголовок IPv6", ext})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out, ok := tcpResetv6(c.pkt); ok {
				t.Errorf("построен RST (%d байт), ожидали отказ", len(out))
			}
		})
	}
}

// captureDevice — устройство, которое только записывает отданные пакеты.
type captureDevice struct {
	mu     sync.Mutex
	writes [][]byte
}

func (d *captureDevice) Read([]byte) (int, error) { return 0, errors.New("нет пакетов") }

func (d *captureDevice) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (d *captureDevice) Name() string { return "capture0" }
func (d *captureDevice) MTU() int     { return testMTU }
func (d *captureDevice) Close() error { return nil }

func (d *captureDevice) recorded() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.writes...)
}

// Мост на отброшенный IPv6-SYN отвечает и ICMPv6, и TCP RST.
func TestBridgeRejectsIPv6SYNWithRST(t *testing.T) {
	dev := &captureDevice{}
	bridge, err := NewBridge(BridgeOptions{
		Device:  dev,
		TUNAddr: tunAddr,
		MTU:     testMTU,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("не используется")
		},
		UDP:  UDPDrop,
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	defer bridge.Close()

	bridge.inject(ipv6SYN("fd00:198:18::1", "2606:4700:4700::1111", 50123, 443, 7))

	var sawICMP, sawRST bool
	for _, pkt := range dev.recorded() {
		if len(pkt) < ipv6HeaderLen || pkt[0]>>4 != 6 {
			continue
		}
		switch pkt[6] {
		case icmpv6NextHeader:
			sawICMP = true
		case tcpNextHeader:
			sawRST = true
		}
	}
	if !sawRST {
		t.Error("мост не отправил TCP RST на IPv6-SYN")
	}
	if !sawICMP {
		t.Error("мост не отправил ICMPv6 на IPv6-SYN")
	}
	stats := bridge.Stats()
	if stats.IPv6Dropped != 1 || stats.IPv6Rejected != 1 {
		t.Errorf("счётчики: отброшено %d, отказов %d (ожидали по 1)", stats.IPv6Dropped, stats.IPv6Rejected)
	}
}
