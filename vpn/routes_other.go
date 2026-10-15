//go:build !linux

package vpn

// Маршруты для других ОС не реализованы: на Windows это netsh или
// CreateIpForwardEntry, на macOS — route и networksetup. Точки адаптации описаны
// в README.
func newRouteManager(opts Options) (RouteManager, error) {
	return nil, ErrUnsupported
}
