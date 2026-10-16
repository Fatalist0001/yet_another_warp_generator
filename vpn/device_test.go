package vpn

import (
	"bytes"
	"os"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// fakeTUN — поддельное устройство пакетов библиотеки tun. Имитирует поведение
// реального Linux-устройства: отдаёт пачку пакетов (так библиотека раскладывает
// GSO-пакет) и запоминает буферы, которые получила на запись.
type fakeTUN struct {
	batchSize int
	// packets — что вернёт ближайшее чтение: один элемент на буфер.
	packets [][]byte

	reads     int
	lastBatch int
	// wbufs — буферы записи без копии: так тест видит, на что указывает
	// переданный библиотеке срез.
	wbufs   [][]byte
	woffset int
}

func (f *fakeTUN) File() *os.File           { return nil }
func (f *fakeTUN) Events() <-chan tun.Event { return nil }
func (f *fakeTUN) MTU() (int, error)        { return 1400, nil }
func (f *fakeTUN) Name() (string, error)    { return "warpgen0", nil }
func (f *fakeTUN) Close() error             { return nil }
func (f *fakeTUN) BatchSize() int           { return f.batchSize }

func (f *fakeTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	f.reads++
	f.lastBatch = len(bufs)
	n := 0
	for i, pkt := range f.packets {
		if i >= len(bufs) {
			break
		}
		if len(bufs[i])-offset < len(pkt) {
			break
		}
		copy(bufs[i][offset:], pkt)
		sizes[i] = len(pkt)
		n++
	}
	// Пачка отдаётся один раз: следующее чтение вернёт следующую (или пусто).
	f.packets = nil
	return n, nil
}

func (f *fakeTUN) Write(bufs [][]byte, offset int) (int, error) {
	f.woffset = offset
	f.wbufs = append(f.wbufs, bufs...)
	total := 0
	for _, b := range bufs {
		total += len(b)
	}
	return total, nil
}

// Чтение пачкой: устройство даёт столько буферов, сколько просит BatchSize, а
// наружу пакеты уходят по одному — иначе GSO-пакет падал бы с ErrTooManySegments.
func TestNativeDeviceReadBatch(t *testing.T) {
	dev := &fakeTUN{batchSize: 4, packets: [][]byte{[]byte("первый"), []byte("второй")}}
	d := newNativeDevice(dev, "warpgen0", 1400)
	if len(d.rbufs) != 4 {
		t.Fatalf("буферов чтения %d, ожидали batchSize устройства (4)", len(d.rbufs))
	}

	buf := make([]byte, 1500)
	n, err := d.Read(buf)
	if err != nil {
		t.Fatalf("первое чтение: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("первый")) {
		t.Errorf("первый пакет = %q", buf[:n])
	}
	if dev.lastBatch != 4 {
		t.Errorf("устройству дали %d буферов, ожидали 4", dev.lastBatch)
	}

	// Второй пакет берётся из той же пачки, без нового чтения.
	n, err = d.Read(buf)
	if err != nil {
		t.Fatalf("второе чтение: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("второй")) {
		t.Errorf("второй пакет = %q", buf[:n])
	}
	if dev.reads != 1 {
		t.Errorf("чтений устройства %d, ожидали 1: пакеты берутся из пачки", dev.reads)
	}

	// Пачка кончилась — читаем следующую.
	dev.packets = [][]byte{[]byte("третий")}
	n, err = d.Read(buf)
	if err != nil {
		t.Fatalf("третье чтение: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("третий")) {
		t.Errorf("третий пакет = %q", buf[:n])
	}
	if dev.reads != 2 {
		t.Errorf("чтений устройства %d, ожидали 2", dev.reads)
	}
}

// Устройство без batchSize (0) читаем одним буфером: пачки ему не нужны.
func TestNativeDeviceReadWithoutBatch(t *testing.T) {
	dev := &fakeTUN{packets: [][]byte{[]byte("один")}}
	d := newNativeDevice(dev, "warpgen0", 1400)
	if len(d.rbufs) != 1 {
		t.Fatalf("буферов чтения %d, ожидали 1", len(d.rbufs))
	}
	buf := make([]byte, 100)
	n, err := d.Read(buf)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("один")) {
		t.Errorf("пакет = %q", buf[:n])
	}
}

// Пакет больше буфера вызывающего — явная ошибка, а не молчаливая обрезка.
func TestNativeDeviceReadTinyBuffer(t *testing.T) {
	dev := &fakeTUN{batchSize: 1, packets: [][]byte{[]byte("длинный-пакет")}}
	d := newNativeDevice(dev, "warpgen0", 1400)
	if _, err := d.Read(make([]byte, 4)); err == nil {
		t.Error("ожидали ошибку о маленьком буфере")
	}
}

// Запись идёт со сдвигом: библиотека дописывает перед пакетом virtio-заголовок,
// поэтому пакет копируется в свой буфер, а не отдаётся по месту.
func TestNativeDeviceWriteKeepsPacketBehindHeader(t *testing.T) {
	dev := &fakeTUN{batchSize: 1}
	d := newNativeDevice(dev, "warpgen0", 1400)

	pkt := []byte("пакет")
	n, err := d.Write(pkt)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(pkt) {
		t.Errorf("записано %d байт, ожидали %d", n, len(pkt))
	}
	if len(dev.wbufs) != 1 {
		t.Fatalf("устройству передали %d буферов, ожидали 1", len(dev.wbufs))
	}
	if dev.woffset != writeHeadroom {
		t.Errorf("сдвиг записи %d, ожидали %d", dev.woffset, writeHeadroom)
	}
	got := dev.wbufs[0]
	if len(got) < dev.woffset+len(pkt) {
		t.Fatalf("буфер записи %d байт — пакет не влез", len(got))
	}
	if !bytes.Equal(got[dev.woffset:dev.woffset+len(pkt)], pkt) {
		t.Errorf("пакет в буфере = %q", got[dev.woffset:])
	}
	// Заголовок перед пакетом — место библиотеки; наш пакет туда не попадает.
	if dev.woffset < 1 {
		t.Error("для virtio-заголовка не оставлено места")
	}

	// Буфер вызывающего не алиасится: библиотека дописывает заголовок в наш буфер.
	for i := range pkt {
		pkt[i] = 'x'
	}
	if !bytes.Equal(got[dev.woffset:dev.woffset+len("пакет")], []byte("пакет")) {
		t.Error("буфер записи алиасится на буфер вызывающего")
	}
}
