package vpn

import (
	"errors"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// Device — устройство пакетов (TUN): один IP-пакет на вызов, без кадровых
// заголовков. Реализации: нативное устройство ОС (Linux — пакет tun из той же
// библиотеки, что и tunnel/) и устройство из готового дескриптора.
//
// Точки адаптации: Android — TunDevice из fd, который отдал VpnService.Builder
// (CreateTUNFromFile); Windows — Wintun; macOS — utun.
type Device interface {
	// Read читает один пакет в p и возвращает его длину. После Close — ошибка.
	Read(p []byte) (int, error)
	// Write отправляет один пакет.
	Write(p []byte) (int, error)
	// Name — фактическое имя интерфейса.
	Name() string
	// MTU — MTU интерфейса.
	MTU() int
	// Close закрывает устройство: в Linux интерфейс при этом исчезает.
	Close() error
}

// nativeDevice адаптирует пакетный API tun.Device к одному пакету на вызов.
// Буферы держим в структуре: читающий и пишущий циклы по одному.
type nativeDevice struct {
	dev   tun.Device
	name  string
	mtu   int
	bufs  [1][]byte
	sizes [1]int
}

func newNativeDevice(dev tun.Device, name string, mtu int) *nativeDevice {
	// Имя могло быть назначено ядром — берём фактическое.
	if actual, err := dev.Name(); err == nil && actual != "" {
		name = actual
	}
	return &nativeDevice{dev: dev, name: name, mtu: mtu}
}

func (d *nativeDevice) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, errors.New("vpn: пустой буфер чтения пакета")
	}
	d.bufs[0] = p
	d.sizes[0] = 0
	n, err := d.dev.Read(d.bufs[:], d.sizes[:], 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("vpn: устройство не вернуло пакет")
	}
	return d.sizes[0], nil
}

func (d *nativeDevice) Write(p []byte) (int, error) {
	d.bufs[0] = p
	n, err := d.dev.Write(d.bufs[:], 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("vpn: пакет не записан устройством")
	}
	return len(p), nil
}

func (d *nativeDevice) Name() string { return d.name }
func (d *nativeDevice) MTU() int     { return d.mtu }

func (d *nativeDevice) Close() error {
	if d.dev == nil {
		return nil
	}
	return d.dev.Close()
}
