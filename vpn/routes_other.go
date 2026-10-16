//go:build !linux

package vpn

import "context"

// Маршруты для других ОС не реализованы: на Windows это netsh или
// CreateIpForwardEntry, на macOS — route и networksetup. Точки адаптации описаны
// в README.
func newRouteManager(opts Options) (RouteManager, error) {
	return nil, ErrUnsupported
}

// RevertTunState — журнал состояния и аварийный откат реализованы только под
// Linux (команды ip и resolvectl); на других ОС откатывать нечего и нечем.
func RevertTunState(ctx context.Context, opts Options) (int, error) {
	return 0, ErrUnsupported
}
