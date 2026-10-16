package vpn

import (
	"errors"
	"fmt"

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

// writeHeadroom — запас перед пакетом при записи в устройство. Пакет tun на Linux
// включает IFF_VNET_HDR: библиотека ждёт перед пакетом заголовок virtio (10 байт),
// заполняет его сама, а offset считает началом пакета. С offset 0 она отвечает
// «invalid offset» и пакет теряется, поэтому пишем со сдвигом: библиотека сама
// использует запас 16 (device.MessageTransportHeaderSize) — держим такой же.
const writeHeadroom = 16

// nativeDevice адаптирует пакетный API tun.Device к одному пакету на вызов.
// Буферы держим в структуре: читающий и пишущий циклы работают одновременно,
// поэтому буферы у них свои — общие поля были бы гонкой.
type nativeDevice struct {
	dev  tun.Device
	name string
	mtu  int

	// На чтение устройство получает пачку буферов (BatchSize): при
	// IFF_VNET_HDR ядро может отдать GSO-пакет, который библиотека раскладывает
	// по нескольким буферам. С одним буфером такая пачка возвращает
	// ErrTooManySegments — и чтение из TUN ломается целиком. Наружу пакеты
	// отдаём по одному, как того требует интерфейс Device.
	rbufs  [][]byte
	rsizes []int
	rnext  int // сколько пакетов текущей пачки уже отдано
	rlast  int // сколько пакетов в текущей пачке
	wbuf   []byte
	wbufs  [1][]byte
}

func newNativeDevice(dev tun.Device, name string, mtu int) *nativeDevice {
	// Имя могло быть назначено ядром — берём фактическое.
	if actual, err := dev.Name(); err == nil && actual != "" {
		name = actual
	}
	// Столько буферов, сколько просит устройство (обычно 128 по 64 КБ — на
	// системный VPN это приемлемо); меньше буферов теряет крупные GSO-пакеты.
	batch := dev.BatchSize()
	if batch < 1 {
		batch = 1
	}
	d := &nativeDevice{dev: dev, name: name, mtu: mtu}
	d.rbufs = make([][]byte, batch)
	d.rsizes = make([]int, batch)
	for i := range d.rbufs {
		d.rbufs[i] = make([]byte, maxPacket)
	}
	return d
}

// Read отдаёт очередной пакет: сначала из уже прочитанной пачки, а когда она
// кончилась — читает новую.
func (d *nativeDevice) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, errors.New("vpn: пустой буфер чтения пакета")
	}
	if d.rnext >= d.rlast {
		n, err := d.dev.Read(d.rbufs, d.rsizes, 0)
		if err != nil {
			return 0, err
		}
		if n <= 0 {
			return 0, errors.New("vpn: устройство не вернуло пакет")
		}
		d.rnext, d.rlast = 0, n
	}
	i := d.rnext
	d.rnext++
	size := d.rsizes[i]
	if size > len(p) {
		return 0, fmt.Errorf("vpn: пакет %d байт не влезает в буфер %d", size, len(p))
	}
	return copy(p, d.rbufs[i][:size]), nil
}

// Write отправляет пакет со сдвигом writeHeadroom: библиотека дописывает перед
// пакетом virtio-заголовок, поэтому пакет нельзя отдавать по месту — копируем его
// в собственный буфер.
func (d *nativeDevice) Write(p []byte) (int, error) {
	size := writeHeadroom + len(p)
	if cap(d.wbuf) < size {
		d.wbuf = make([]byte, size)
	}
	buf := d.wbuf[:size]
	copy(buf[writeHeadroom:], p)
	d.wbufs[0] = buf
	n, err := d.dev.Write(d.wbufs[:], writeHeadroom)
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
