//go:build windows

package vpn

import (
	"fmt"
	"os"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// newDevice создаёт TUN через Wintun — адаптер Layer 3, который отдаёт пакеты
// без кадровых заголовков. Wintun требует прав администратора, а wintun.dll
// должна лежать рядом с warpgen.exe (её кладёт scripts/build-windows.sh).
func newDevice(name string, mtu int) (Device, error) {
	if name == "" {
		name = DefaultTUNName
	}
	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf(
			"vpn: создание TUN %s: %w (нужны права администратора и wintun.dll рядом с warpgen.exe)",
			name, err)
	}
	return newNativeDevice(dev, name, mtu), nil
}

// DeviceFromFile под Windows не реализован: fd TUN отдаёт только Android
// VpnService. Точка адаптации оставлена для единообразия интерфейса.
func DeviceFromFile(*os.File, string, int) (Device, error) {
	return nil, ErrUnsupported
}
