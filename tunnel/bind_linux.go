//go:build linux

package tunnel

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// BindToInterfaceSupported сообщает, поддерживается ли привязка сокета к интерфейсу.
const BindToInterfaceSupported = true

// deviceControl возвращает Control-функцию, привязывающую сокет к интерфейсу
// (SO_BINDTODEVICE). Нужен CAP_NET_RAW; при ошибке штатно возвращаем её наверх.
func deviceControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var opErr error
		if err := c.Control(func(fd uintptr) {
			opErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		}); err != nil {
			return err
		}
		return opErr
	}
}
