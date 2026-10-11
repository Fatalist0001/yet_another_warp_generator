// Package tunnel — userspace WireGuard/AmneziaWG-туннель до WARP.
//
// Туннель целиком живёт в процессе: обычные UDP-сокеты + netstack, без root,
// без TUN, без VpnService и без изменения маршрутов системы. За основу взят
// github.com/vernette/warpscout (MIT).
package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/netstack"
)

// WARP-safe константы туннеля.
const (
	DefaultMTU     = 1280
	DefaultDNS     = "1.1.1.1"
	defaultKeepal  = 25
	handshakeKey   = "last_handshake_time_sec="
	pollStart      = 2 * time.Millisecond
	pollMax        = 50 * time.Millisecond
	DefaultTimeout = 3 * time.Second
)

// Options — параметры userspace-туннеля.
type Options struct {
	// PrivateKey — приватный ключ клиента (base64).
	PrivateKey string
	// PeerPublicKey — публичный ключ сервера Cloudflare (base64).
	PeerPublicKey string
	// LocalV4 — адрес интерфейса IPv4 из аккаунта.
	LocalV4 string
	// LocalV6 — адрес интерфейса IPv6 из аккаунта.
	LocalV6 string
	// DNS — резолвер(ы) netstack. По умолчанию 1.1.1.1.
	DNS []string
	// MTU — MTU туннеля (0 → 1280).
	MTU int
	// Jc / Jmin / Jmax / I1 — параметры обфускации AmneziaWG.
	Jc   int
	Jmin int
	Jmax int
	I1   string
	// Interface — привязать исходящий UDP-сокет к интерфейсу (linux, нужен root).
	Interface string
}

// Tunnel — поднятый userspace-туннель. Один туннель переиспользуется для
// перебора endpoint: Handshake переустанавливает пир.
type Tunnel struct {
	dev        *device.Device
	tnet       *netstack.Net
	client     *http.Client
	peerPublic string
}

// New поднимает туннель с заданными параметрами (без системного VPN).
func New(opts Options) (*Tunnel, error) {
	if opts.PrivateKey == "" {
		return nil, errors.New("tunnel: не задан PrivateKey")
	}
	if opts.PeerPublicKey == "" {
		return nil, errors.New("tunnel: не задан PeerPublicKey")
	}
	mtu := opts.MTU
	if mtu == 0 {
		mtu = DefaultMTU
	}

	locals, err := parseAddrs(opts.LocalV4, opts.LocalV6)
	if err != nil {
		return nil, err
	}
	if len(locals) == 0 {
		return nil, errors.New("tunnel: не задан адрес интерфейса (LocalV4/LocalV6)")
	}
	dnsNames := opts.DNS
	if len(dnsNames) == 0 {
		dnsNames = []string{DefaultDNS}
	}
	dns, err := parseAddrs(dnsNames...)
	if err != nil {
		return nil, fmt.Errorf("tunnel: dns: %w", err)
	}

	tunDev, tnet, err := netstack.CreateNetTUN(locals, dns, mtu)
	if err != nil {
		return nil, fmt.Errorf("tunnel: netstack: %w", err)
	}

	base, err := baseUAPI(opts)
	if err != nil {
		return nil, err
	}

	bind := conn.Bind(newDeviceBind(opts.Interface))
	dev := device.NewDevice(tunDev, bind, device.NewLogger(device.LogLevelSilent, ""))
	if err := dev.IpcSet(base); err != nil {
		dev.Close()
		return nil, fmt.Errorf("tunnel: IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("tunnel: Up: %w", err)
	}

	// netstack резолвит hostname сам, поэтому DialContext получает полный адрес.
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return tnet.DialContext(ctx, network, address)
		},
	}
	return &Tunnel{
		dev:        dev,
		tnet:       tnet,
		client:     &http.Client{Transport: transport},
		peerPublic: opts.PeerPublicKey,
	}, nil
}

// Close освобождает ресурсы туннеля.
func (t *Tunnel) Close() {
	if t.dev != nil {
		t.dev.Close()
	}
}

// Handshake переустанавливает пир на endpoint и ждёт завершения хендшейка.
// Возвращает true, если хендшейк прошёл до таймаута.
func (t *Tunnel) Handshake(ctx context.Context, endpoint string, timeout time.Duration) bool {
	peer, err := t.peerUAPI(endpoint)
	if err != nil {
		return false
	}
	if err := t.dev.IpcSet(peer); err != nil {
		return false
	}
	return t.waitHandshake(ctx, timeout)
}

func (t *Tunnel) waitHandshake(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	wait := pollStart
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		conf, err := t.dev.IpcGet()
		if err == nil && handshakeDone(conf) {
			return true
		}
		time.Sleep(wait)
		if wait < pollMax {
			wait *= 2
		}
	}
	return false
}

// handshakeDone — в выводе IpcGet есть ненулевой last_handshake_time_sec.
func handshakeDone(conf string) bool {
	i := strings.Index(conf, handshakeKey)
	if i < 0 {
		return false
	}
	v := i + len(handshakeKey)
	return v < len(conf) && conf[v] != '0'
}

// baseUAPI — общая часть UAPI-конфига устройства.
func baseUAPI(opts Options) (string, error) {
	privHex, err := base64ToHex(opts.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("tunnel: private key: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privHex)

	// Обфускация: обычный сервер Cloudflare знает только про junk и I1.
	if opts.Jc != 0 || opts.Jmin != 0 || opts.Jmax != 0 {
		fmt.Fprintf(&b, "jc=%d\n", opts.Jc)
		fmt.Fprintf(&b, "jmin=%d\n", opts.Jmin)
		fmt.Fprintf(&b, "jmax=%d\n", opts.Jmax)
	}
	if opts.I1 != "" {
		fmt.Fprintf(&b, "i1=%s\n", opts.I1)
	}
	return b.String(), nil
}

// peerUAPI — UAPI-конфиг пира для выбранного endpoint.
func (t *Tunnel) peerUAPI(endpoint string) (string, error) {
	pubHex, err := base64ToHex(t.peerPublic)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "replace_peers=true\n")
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	fmt.Fprintf(&b, "allowed_ip=::/0\n")
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", defaultKeepal)
	return b.String(), nil
}

func base64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func parseAddrs(names ...string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range names {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q не является адресом: %w", s, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// EndpointAddr склеивает ip:port (в т.ч. корректно для IPv6).
func EndpointAddr(ip netip.Addr, port int) string {
	return netip.AddrPortFrom(ip, uint16(port)).String()
}

// DefaultDNSAddr — адрес резолвера по умолчанию, используется как цель замера
// задержки внутри туннеля.
func DefaultDNSAddr() netip.Addr { return netip.MustParseAddr(DefaultDNS) }

// DialContext открывает tcp/udp-соединение через туннель. Hostname в address
// резолвится внутри туннеля (netstack), а не локальным резолвером.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return t.tnet.DialContext(ctx, network, address)
}
