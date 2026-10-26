//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// receiveTunFD ждёт на unix-сокете дескриптор TUN, переданный через SCM_RIGHTS.
//
// Android-обёртка (VpnService) создаёт TUN, запускает CLI и отправляет fd по
// сокету. Сам CLI не может открыть /dev/net/tun без root, поэтому fd — это
// единственный способ передать VpnService-устройство в Go-пайплайн.
func receiveTunFD(path string) (*os.File, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("cli: не задан unix-сокет для fd TUN")
	}
	// Сокет от прошлого запуска мог остаться: ListenUnix не перезаписывает файл.
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("cli: unix-сокет %s: %w", path, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(path)
	}()

	conn, err := ln.AcceptUnix()
	if err != nil {
		return nil, fmt.Errorf("cli: ожидание fd TUN: %w", err)
	}
	defer conn.Close()

	raw, err := conn.File()
	if err != nil {
		return nil, fmt.Errorf("cli: дескриптор unix-сокета: %w", err)
	}
	defer raw.Close()

	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(int(raw.Fd()), buf, oob, 0)
	if err != nil {
		return nil, fmt.Errorf("cli: приём fd TUN: %w", err)
	}
	if oobn == 0 {
		return nil, errors.New("cli: unix-сокет не передал fd TUN")
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("cli: разбор fd TUN: %w", err)
	}
	for i := range msgs {
		fds, err := unix.ParseUnixRights(&msgs[i])
		if err != nil {
			continue
		}
		if len(fds) > 0 {
			return os.NewFile(uintptr(fds[0]), "tun"), nil
		}
	}
	return nil, errors.New("cli: в unix-сокете нет fd TUN")
}
