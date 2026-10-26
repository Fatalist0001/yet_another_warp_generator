//go:build !linux && !windows

package vpn

import (
	"os"
)

// Реализации для остальных ОС не сделаны: на macOS нужен utun. Linux и Windows
// лежат в отдельных файлах (tun_linux.go, tun_windows.go). Здесь честная
// ошибка, а не молчаливая поломка.

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
