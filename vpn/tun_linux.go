//go:build linux

package vpn

import (
	"fmt"
	"os"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// newDevice создаёт TUN-интерфейс заданного имени и MTU. Устройство делает пакет
// tun из библиотеки amneziawg-go — вручную ioctl не дёргаем.
func newDevice(name string, mtu int) (Device, error) {
	if name == "" {
		name = DefaultTUNName
	}
	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("vpn: создание TUN %s: %w (нужны права root и /dev/net/tun)", name, err)
	}
	return newNativeDevice(dev, name, mtu), nil
}

// DeviceFromFile поднимает устройство из готового дескриптора — точка адаптации
// под Android: fd отдаёт VpnService.Builder, и права root не нужны. Готовый Device
// можно передать в vpn.Options.Device.
func DeviceFromFile(file *os.File, name string, mtu int) (Device, error) {
	if file == nil {
		return nil, fmt.Errorf("vpn: не задан дескриптор TUN")
	}
	dev, err := tun.CreateTUNFromFile(file, mtu)
	if err != nil {
		return nil, fmt.Errorf("vpn: TUN из дескриптора: %w", err)
	}
	return newNativeDevice(dev, name, mtu), nil
}
