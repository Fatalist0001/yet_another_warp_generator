//go:build !linux && !windows

package vpn

import "context"

// Маршруты для остальных ОС не реализованы: на macOS это route и networksetup
// (Linux — routes_linux.go, Windows — routes_windows.go).
func newRouteManager(opts Options) (RouteManager, error) {
	return nil, ErrUnsupported
}

// RevertTunState — журнал состояния и аварийный откат реализованы только под
// Linux (команды ip и resolvectl); на других ОС откатывать нечего и нечем.
func RevertTunState(ctx context.Context, opts Options) (int, error) {
	return 0, ErrUnsupported
}

// ReadCurrentDNS на не-Linux не реализован.
func ReadCurrentDNS(ctx context.Context, exec Executor) (DNSInfo, error) {
	return DNSInfo{}, ErrUnsupported
}
