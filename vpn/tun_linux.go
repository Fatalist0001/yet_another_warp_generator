//go:build linux

package vpn

import (
	"fmt"
	"os"

	"github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/sys/unix"
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

	// CreateTUNFromFile зовёт SIOCSIFMTU и netlink: на Android приложение не
	// имеет CAP_NET_ADMIN, поэтому это «permission denied». VpnService уже
	// задал MTU через Builder, а пакеты идут без virtio-заголовка — берём
	// неконтролируемый вариант и не трогаем интерфейс через ioctl.
	//
	// Дескриптор дублируем: CreateUnmonitoredTUNFromFD забирает fd себе,
	// а исходный os.File закрываем, чтобы его финализатор не закрыл чужой fd.
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("vpn: дублирование дескриптора TUN: %w", err)
	}
	unix.CloseOnExec(fd)
	_ = file.Close()

	dev, actual, err := tun.CreateUnmonitoredTUNFromFD(fd)
	if err != nil {
		return nil, fmt.Errorf("vpn: TUN из дескриптора: %w", err)
	}
	if name == "" {
		name = actual
	}
	return newNativeDevice(dev, name, mtu), nil
}
