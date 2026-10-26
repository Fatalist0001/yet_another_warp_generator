//go:build windows

package vpn

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// checkPrivileges — Wintun создаёт сетевой адаптер, а маршруты и DNS меняются
// только с повышенными правами. GUI запускается с манифестом
// requireAdministrator, поэтому дочерний warpgen наследует права; CLI из
// обычного терминала обязан упасть с понятным сообщением.
func checkPrivileges() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("vpn: режим --tun создаёт сетевой адаптер и меняет маршруты — запустите программу от имени администратора (UAC)")
	}
	return nil
}
