package vpn

import "encoding/binary"

// ICMP-отказы вместо тихого отбрасывания.
//
// Мост не проксирует IPv6 и не-DNS UDP. Раньше такие пакеты просто исчезали, и
// приложение узнавало об этом только по собственному таймауту — от секунд до
// минут. Снаружи это выглядит так, будто сеть «включилась не сразу»: Telegram
// висит на мёртвом IPv6-соединении, браузер ждёт QUIC, часть программ не
// восстанавливается вовсе, пока туннель не переподключат.
//
// Поэтому на отброшенный пакет приходит ошибка ICMP: IPv6 — «нет маршрута»,
// не-DNS UDP — «порт недоступен». Ядро сразу роняет попытку (EHOSTUNREACH /
// ECONNREFUSED), и клиент переходит на IPv4 или TCP без ожидания. Проксирование
// при этом не меняется: трафик по-прежнему не идёт через SOCKS5.
const (
	// icmpv6NextHeader — номер ICMPv6 в заголовке IPv6.
	icmpv6NextHeader = 58
	// icmpv6DestinationUnreachable / icmpv6CodeNoRoute — «пункт назначения
	// недоступен, маршрута нет».
	icmpv6DestinationUnreachable = 1
	icmpv6CodeNoRoute            = 0

	// icmpv4DestinationUnreachable / icmpv4CodePortUnreachable — «пункт
	// назначения недоступен, порт недоступен».
	icmpv4DestinationUnreachable = 3
	icmpv4CodePortUnreachable    = 3
	// icmpv4ProtocolNumber — номер ICMP в заголовке IPv4.
	icmpv4ProtocolNumber = 1

	// icmpv6MaxPacket — предел ответа целиком: минимальный MTU IPv6, меньше
	// которого пакет никуда не пройдёт.
	icmpv6MaxPacket = 1280
	// icmpv4MaxPacket — классический предел для ICMPv4-ошибки.
	icmpv4MaxPacket = 576

	ipv6HeaderLen = 40
	ipv4HeaderLen = 20
	icmpHeaderLen = 8
)

// icmpv6NoRoute собирает ICMPv6 Destination Unreachable (code 0) в ответ на
// IPv6-пакет, который мост не может провести. Возвращает готовый IPv6-пакет для
// записи в TUN и признак, что ответ уместен.
//
// Не отвечаем: на сам ICMPv6 (иначе получится переписка ошибок), на пакеты с
// неопределённым или многоадресным источником (DAD и служебные сообщения стека)
// и на многоадресный адрес назначения — это не соединения приложений.
func icmpv6NoRoute(pkt []byte) ([]byte, bool) {
	if len(pkt) < ipv6HeaderLen || pkt[0]>>4 != 6 {
		return nil, false
	}
	if pkt[6] == icmpv6NextHeader {
		return nil, false
	}
	var src, dst [16]byte
	copy(src[:], pkt[8:24])
	copy(dst[:], pkt[24:40])
	if src == ([16]byte{}) {
		return nil, false
	}
	if src[0] == 0xff || dst[0] == 0xff {
		return nil, false
	}
	// link-local источник — связь с соседями, а не трафик приложения.
	if src[0] == 0xfe && src[1]&0xc0 == 0x80 {
		return nil, false
	}

	quote := pkt
	if maxQuote := icmpv6MaxPacket - ipv6HeaderLen - icmpHeaderLen; len(quote) > maxQuote {
		quote = quote[:maxQuote]
	}
	icmpLen := icmpHeaderLen + len(quote)
	out := make([]byte, ipv6HeaderLen+icmpLen)
	out[0] = 0x60
	binary.BigEndian.PutUint16(out[4:6], uint16(icmpLen))
	out[6] = icmpv6NextHeader
	out[7] = 64
	// Отвечаем от имени адресата исходного пакета: только такой источник ядро
	// сопоставит с соединением приложения.
	copy(out[8:24], dst[:])
	copy(out[24:40], src[:])
	out[ipv6HeaderLen] = icmpv6DestinationUnreachable
	out[ipv6HeaderLen+1] = icmpv6CodeNoRoute
	copy(out[ipv6HeaderLen+icmpHeaderLen:], quote)
	body := out[ipv6HeaderLen:]
	binary.BigEndian.PutUint16(body[2:4], icmpv6Checksum(out[8:24], out[24:40], body))
	return out, true
}

// icmpv4PortUnreachable собирает ICMPv4 Destination Unreachable (code 3) в ответ
// на UDP, который мост не проксирует. Так приложение узнаёт об отказе сразу и
// откатывается с QUIC на TCP, не дожидаясь таймаута.
func icmpv4PortUnreachable(pkt []byte) ([]byte, bool) {
	if len(pkt) < ipv4HeaderLen || pkt[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < ipv4HeaderLen || len(pkt) < ihl {
		return nil, false
	}
	if pkt[9] == icmpv4ProtocolNumber {
		return nil, false
	}
	var src, dst [4]byte
	copy(src[:], pkt[12:16])
	copy(dst[:], pkt[16:20])
	// 0.0.0.0 — адрес до настройки интерфейса (DHCP): отвечать некому.
	if src == ([4]byte{}) {
		return nil, false
	}

	quote := pkt
	if maxQuote := icmpv4MaxPacket - ipv4HeaderLen - icmpHeaderLen; len(quote) > maxQuote {
		quote = quote[:maxQuote]
	}
	icmpLen := icmpHeaderLen + len(quote)
	out := make([]byte, ipv4HeaderLen+icmpLen)
	out[0] = 0x45
	binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
	out[8] = 64
	out[9] = icmpv4ProtocolNumber
	copy(out[12:16], dst[:])
	copy(out[16:20], src[:])
	binary.BigEndian.PutUint16(out[10:12], inetChecksum(out[:ipv4HeaderLen]))
	out[ipv4HeaderLen] = icmpv4DestinationUnreachable
	out[ipv4HeaderLen+1] = icmpv4CodePortUnreachable
	copy(out[ipv4HeaderLen+icmpHeaderLen:], quote)
	binary.BigEndian.PutUint16(out[ipv4HeaderLen+2:ipv4HeaderLen+4], inetChecksum(out[ipv4HeaderLen:]))
	return out, true
}

// icmpv6Checksum — контрольная сумма ICMPv6: считается по псевдозаголовку
// (источник, адресат, длина, следующий заголовок) и самому сообщению.
func icmpv6Checksum(src, dst, msg []byte) uint16 {
	var sum uint32
	sum = addWords(sum, src)
	sum = addWords(sum, dst)
	sum += uint32(len(msg))
	sum += icmpv6NextHeader
	return foldWords(addWords(sum, msg))
}

// inetChecksum — контрольная сумма заголовка IPv4 и сообщения ICMPv4.
func inetChecksum(b []byte) uint16 {
	return foldWords(addWords(0, b))
}

// addWords складывает байты 16-битными словами; нечётный хвост дополняется нулём.
func addWords(sum uint32, b []byte) uint32 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	return sum
}

// foldWords сворачивает сумму переносов и берёт обратный код.
func foldWords(sum uint32) uint16 {
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}
