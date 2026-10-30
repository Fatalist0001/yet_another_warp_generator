package vpn

import "encoding/binary"

// TCP-сброс вместо ожидания на IPv6.
//
// Мост не проводит IPv6 — и на такой пакет уходит ICMPv6 «нет маршрута»
// (icmp.go). Но ICMP — вспомогательный протокол: Windows/Firewall может его не
// доставить приложению, и тогда попытка соединения висит до собственного
// таймаута (десятки секунд). Приложение, которое предпочло IPv6 (Telegram и
// часть Electron-клиентов), всё это время выглядит «без сети», хотя IPv4 через
// тот же мост работает.
//
// Поэтому на IPv6-SYN отвечаем ещё и TCP RST|ACK: это обычный пакет внутри
// соединения, и TCP-стек клиента обрабатывает его без участия ICMP — соединение
// падает сразу (connection refused), и приложение мгновенно уходит на IPv4.
//
// Отвечаем только на попытку соединения (SYN без ACK): на прочие «чужие»
// сегменты ответ ничего не улучшает — соединения с мостом всё равно нет.
const (
	// tcpNextHeader — номер TCP в заголовке IPv6.
	tcpNextHeader = 6
	// tcpHeaderLen — минимальный заголовок TCP.
	tcpHeaderLen = 20

	// Флаги TCP.
	tcpFlagACK = 0x10
	tcpFlagRST = 0x04
	tcpFlagSYN = 0x02
)

// tcpResetv6 собирает IPv6-пакет с TCP RST|ACK в ответ на IPv6-SYN, который мост
// не может провести. Возвращает готовый пакет для записи в TUN и признак, что
// ответ уместен.
//
// Не отвечаем: не на SYN (или на SYN с ACK — это уже ответ), на пакеты с
// неопределённым, многоадресным или link-local источником и с многоадресным
// адресатом, а также на пакеты с расширенными заголовками IPv6 (их разбор не
// нужен: такие сегменты шлёт не приложение).
func tcpResetv6(pkt []byte) ([]byte, bool) {
	if len(pkt) < ipv6HeaderLen || pkt[0]>>4 != 6 {
		return nil, false
	}
	// Расширенные заголовки IPv6 пропускаем: работаем только с чистым TCP.
	if pkt[6] != tcpNextHeader {
		return nil, false
	}
	var src, dst [16]byte
	copy(src[:], pkt[8:24])
	copy(dst[:], pkt[24:40])
	if src == ([16]byte{}) || src[0] == 0xff || dst[0] == 0xff {
		return nil, false
	}
	if src[0] == 0xfe && src[1]&0xc0 == 0x80 {
		return nil, false
	}

	seg := pkt[ipv6HeaderLen:]
	if len(seg) < tcpHeaderLen {
		return nil, false
	}
	// payload length из заголовка IPv6: хвост буфера может быть длиннее пакета.
	if plen := int(binary.BigEndian.Uint16(pkt[4:6])); plen >= tcpHeaderLen && plen < len(seg) {
		seg = seg[:plen]
	}
	if len(seg) < tcpHeaderLen {
		return nil, false
	}
	if seg[13]&tcpFlagSYN == 0 || seg[13]&tcpFlagACK != 0 {
		return nil, false
	}
	// Номер последовательности исходного SYN: подтверждаем его первым байтом.
	ack := binary.BigEndian.Uint32(seg[4:8]) + 1

	out := make([]byte, ipv6HeaderLen+tcpHeaderLen)
	out[0] = 0x60
	binary.BigEndian.PutUint16(out[4:6], tcpHeaderLen)
	out[6] = tcpNextHeader
	out[7] = 64
	// Отвечаем от имени адресата исходного пакета: только такой ответ клиент
	// сопоставит со своим соединением.
	copy(out[8:24], dst[:])
	copy(out[24:40], src[:])

	reset := out[ipv6HeaderLen:]
	binary.BigEndian.PutUint16(reset[0:2], binary.BigEndian.Uint16(seg[2:4])) // порт источника = порт адресата
	binary.BigEndian.PutUint16(reset[2:4], binary.BigEndian.Uint16(seg[0:2])) // порт адресата = порт источника
	binary.BigEndian.PutUint32(reset[4:8], 0)                                 // seq
	binary.BigEndian.PutUint32(reset[8:12], ack)                              // ack
	reset[12] = 5 << 4                                                        // длина заголовка: 5 слов
	reset[13] = tcpFlagRST | tcpFlagACK
	binary.BigEndian.PutUint16(reset[14:16], 0) // окно
	binary.BigEndian.PutUint16(reset[16:18], tcpv6Checksum(out[8:24], out[24:40], reset))
	return out, true
}

// tcpv6Checksum — контрольная сумма TCP по IPv6-псевдозаголовку.
func tcpv6Checksum(src, dst, seg []byte) uint16 {
	var sum uint32
	sum = addWords(sum, src)
	sum = addWords(sum, dst)
	sum += uint32(len(seg))
	sum += tcpNextHeader
	return foldWords(addWords(sum, seg))
}
