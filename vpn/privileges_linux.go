//go:build linux && !android

package vpn

import (
	"fmt"
	"os"
)

// checkPrivileges — маршруты и TUN меняются только от root.
func checkPrivileges() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("vpn: режим --tun меняет маршруты и создаёт интерфейс — запустите с правами root (sudo)")
	}
	return nil
}
