//go:build !linux

package tunnel

import "syscall"

// BindToInterfaceSupported сообщает, поддерживается ли привязка сокета к интерфейсу.
const BindToInterfaceSupported = false

func deviceControl(string) func(network, address string, c syscall.RawConn) error { return nil }
