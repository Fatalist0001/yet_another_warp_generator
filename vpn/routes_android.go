//go:build android

package vpn

import (
	"context"
	"net/netip"
)

// androidRoutes — заглушка: адрес, маршруты и DNS задаёт VpnService.Builder.
// Приложение исключено из VPN (addDisallowedApplication), поэтому отдельный
// protect для его сокетов не нужен.
type androidRoutes struct{}

func newRouteManager(Options) (RouteManager, error) {
	return androidRoutes{}, nil
}

func (androidRoutes) Apply(context.Context, Plan) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (androidRoutes) SetEndpoint(context.Context, netip.Addr) error { return nil }

func (androidRoutes) Revert(context.Context) error { return nil }

// RevertTunState — на Android откатывать нечего: маршруты живут вместе с
// VpnService и исчезают при его остановке.
func RevertTunState(context.Context, Options) (int, error) {
	return 0, ErrUnsupported
}

// ReadCurrentDNS — системный DNS задаёт VpnService, читать его из CLI нечего.
func ReadCurrentDNS(context.Context, Executor) (DNSInfo, error) {
	return DNSInfo{Warning: "на Android DNS задаёт VpnService"}, nil
}
