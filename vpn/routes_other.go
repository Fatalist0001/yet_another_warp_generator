//go:build !linux && !windows

package vpn

import (
	"context"
	"os"
	"path/filepath"
)

// Маршруты для остальных ОС не реализованы: на macOS это route и networksetup
// (Linux — routes_linux.go, Windows — routes_windows.go).
func newRouteManager(opts Options) (RouteManager, error) {
	return nil, ErrUnsupported
}

// platformStateDir — каталог журнала отката там, где откат не реализован:
// нужен только чтобы statePath собирался на всех платформах.
func platformStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".warpgen", "state")
	}
	return filepath.Join(home, ".warpgen", "state")
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
