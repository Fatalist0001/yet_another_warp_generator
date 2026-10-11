package tunnel

import (
	"context"
	"net"
	"net/netip"
	"strconv"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

// deviceBind — userspace UDP bind для wireguard-go: обычный UDP-сокет, без TUN,
// без root. Порт изолирован в platform-файлах (deviceControl).
type deviceBind struct {
	iface string
	conn  *net.UDPConn
}

type sourceEndpoint struct{ dst netip.AddrPort }

func newDeviceBind(iface string) *deviceBind { return &deviceBind{iface: iface} }

func (b *deviceBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	var lc net.ListenConfig
	if b.iface != "" {
		lc.Control = deviceControl(b.iface)
	}
	pc, err := lc.ListenPacket(context.Background(), "udp", net.JoinHostPort("", strconv.Itoa(int(port))))
	if err != nil {
		return nil, 0, err
	}
	c := pc.(*net.UDPConn)
	b.conn = c
	actual := uint16(c.LocalAddr().(*net.UDPAddr).Port)
	return []conn.ReceiveFunc{b.receive}, actual, nil
}

func (b *deviceBind) receive(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	n, addr, err := b.conn.ReadFromUDPAddrPort(bufs[0])
	if err != nil {
		// Единственная ожидаемая ошибка чтения — закрытие сокета; любую трактуем
		// как терминальную, чтобы receive-goroutine устройства завершилась штатно.
		return 0, net.ErrClosed
	}
	sizes[0] = n
	eps[0] = &sourceEndpoint{dst: addr}
	return 1, nil
}

func (b *deviceBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	dst := ep.(*sourceEndpoint).dst
	for _, buf := range bufs {
		if _, err := b.conn.WriteToUDPAddrPort(buf, dst); err != nil {
			return err
		}
	}
	return nil
}

func (b *deviceBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	addr, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &sourceEndpoint{dst: addr}, nil
}

func (b *deviceBind) Close() error {
	if b.conn == nil {
		return nil
	}
	return b.conn.Close()
}

func (b *deviceBind) SetMark(uint32) error { return nil }

func (b *deviceBind) BatchSize() int { return 1 }

func (e *sourceEndpoint) ClearSrc()           {}
func (e *sourceEndpoint) SrcToString() string { return "" }
func (e *sourceEndpoint) DstToString() string { return e.dst.String() }
func (e *sourceEndpoint) DstToBytes() []byte  { b, _ := e.dst.MarshalBinary(); return b }
func (e *sourceEndpoint) DstIP() netip.Addr   { return e.dst.Addr() }
func (e *sourceEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }
