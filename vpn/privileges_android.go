//go:build android

package vpn

// checkPrivileges — на Android права и маршруты задаёт VpnService, root не нужен.
func checkPrivileges() error { return nil }
