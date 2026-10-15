//go:build !linux

package vpn

import (
	"os"
)

// Реализации для других ОС не сделаны: на Windows нужен Wintun, на macOS —
// utun. Точки адаптации описаны в README (раздел «Точки адаптации»), поэтому
// здесь честная ошибка, а не молчаливая поломка.

func newDevice(name string, mtu int) (Device, error) {
	return nil, ErrUnsupported
}

// DeviceFromFile — точка адаптации под Android (VpnService.Builder отдаёт fd).
// Реализована только для Linux.
func DeviceFromFile(file *os.File, name string, mtu int) (Device, error) {
	return nil, ErrUnsupported
}

func checkPrivileges() error {
	return ErrUnsupported
}
