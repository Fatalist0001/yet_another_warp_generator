package vpn

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
	"syscall"
	"time"
)

// Коды ответа SOCKS5 (RFC 1928).
const (
	socksVersion = 5
	socksConnect = 1
	socksNoAuth  = 0

	socksRepSuccess       = 0
	socksRepNotAllowed    = 2
	socksRepHostUnreach   = 4
	socksRepConnRefused   = 5
	socksRepTTLExpired    = 6
	socksRepCmdNotSupport = 7
	socksRepAddrNotSupp   = 8
)

// dialSocks5 открывает TCP-соединение через SOCKS5 (CONNECT без аутентификации).
//
// Свой дозвон вместо x/net/proxy нужен ради полузакрытия: библиотека прячет
// исходный net.Conn за своей обёрткой, и CloseWrite клиента не доходил бы до
// удалённой стороны — серверы, ждущие EOF, повисли бы до таймаута простоя.
func dialSocks5(ctx context.Context, socks, address string, protect func(fd int) error) (net.Conn, error) {
	req, err := socksRequest(address)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", socks)
	if err != nil {
		return nil, fmt.Errorf("vpn: SOCKS5 %s: %w", socks, err)
	}
	if err := protectConn(conn, protect); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: защита сокета: %w", socks, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	// Ждём разрыва контекста, пока идёт обмен с прокси: соединение уже открыто,
	// само оно о разрыве не узнает.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	// Приветствие: версия, число методов, «без аутентификации».
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: приветствие: %w", socks, err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: ответ на приветствие: %w", socks, err)
	}
	if greeting[0] != socksVersion {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: версия %d вместо %d", socks, greeting[0], socksVersion)
	}
	if greeting[1] != socksNoAuth {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: требует аутентификацию (метод %d), а warp-plus её не использует", socks, greeting[1])
	}

	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: запрос CONNECT %s: %w", socks, address, err)
	}
	if err := readSocksReply(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vpn: SOCKS5 %s: %s: %w", socks, address, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// protectConn передаёт дескриптор сокета внешней защите (Android:
// VpnService.protect). Дескриптор берётся через SyscallConn: File() перевёл бы
// сокет в блокирующий режим и отдал копию.
func protectConn(conn net.Conn, protect func(fd int) error) error {
	if protect == nil {
		return nil
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("соединение %T не отдаёт дескриптор", conn)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var protectErr error
	if err := raw.Control(func(fd uintptr) { protectErr = protect(int(fd)) }); err != nil {
		return err
	}
	return protectErr
}

// socksRequest собирает запрос CONNECT: версия, команда, резерв, адрес, порт.
func socksRequest(address string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return nil, fmt.Errorf("vpn: адрес %q не разбирается: %w", address, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("vpn: некорректный порт в адресе %q", address)
	}

	out := []byte{socksVersion, socksConnect, 0}
	switch addr, aerr := netip.ParseAddr(host); {
	case aerr == nil && addr.Is4():
		ip := addr.As4()
		out = append(out, 1)
		out = append(out, ip[:]...)
	case aerr == nil:
		ip := addr.As16()
		out = append(out, 4)
		out = append(out, ip[:]...)
	case len(host) > 255:
		return nil, fmt.Errorf("vpn: имя %q длиннее 255 байт", host)
	default:
		out = append(out, 3, byte(len(host)))
		out = append(out, host...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	return append(out, portBytes[:]...), nil
}

// readSocksReply читает ответ на CONNECT и превращает код в понятную ошибку.
func readSocksReply(conn net.Conn) error {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return fmt.Errorf("чтение ответа: %w", err)
	}
	if head[0] != socksVersion {
		return fmt.Errorf("версия ответа %d вместо %d", head[0], socksVersion)
	}
	if err := skipSocksAddr(conn, head[3]); err != nil {
		return err
	}
	if head[1] != socksRepSuccess {
		return socksReplyError(head[1])
	}
	return nil
}

// skipSocksAddr дочитывает привязанный адрес и порт прокси (они нам не нужны,
// но оставлять их в потоке нельзя — дальше пойдут данные).
func skipSocksAddr(conn net.Conn, atyp byte) error {
	if err := skipSocksHost(conn, atyp); err != nil {
		return err
	}
	_, err := io.ReadFull(conn, make([]byte, 2))
	return err
}

// skipSocksHost дочитывает привязанный адрес прокси без порта.
func skipSocksHost(conn net.Conn, atyp byte) error {
	switch atyp {
	case 1:
		_, err := io.ReadFull(conn, make([]byte, 4))
		return err
	case 4:
		_, err := io.ReadFull(conn, make([]byte, 16))
		return err
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		_, err := io.ReadFull(conn, make([]byte, int(length[0])))
		return err
	default:
		return fmt.Errorf("неизвестный тип адреса %d в ответе", atyp)
	}
}

// socksReplyError переводит код отказа SOCKS5 в текст.
func socksReplyError(rep byte) error {
	switch rep {
	case socksRepNotAllowed:
		return errors.New("прокси запретил соединение (правило)")
	case socksRepHostUnreach:
		return errors.New("хост недоступен")
	case socksRepConnRefused:
		return errors.New("соединение отклонено")
	case socksRepTTLExpired:
		return errors.New("истёк TTL")
	case socksRepCmdNotSupport, socksRepAddrNotSupp:
		return fmt.Errorf("команда или тип адреса не поддержан (код %d)", rep)
	default:
		return fmt.Errorf("отказ SOCKS5 (код %d)", rep)
	}
}
