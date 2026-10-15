package chain

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

// IdentityFileName — имя файла identity, которое читает warp-plus.
const identityFileName = "wgcf-identity.json"

// identityDirName — подкаталог cache-dir, откуда warp-plus грузит identity.
const identityDirName = "primary"

// reservedClientID — base64 от трёх нулевых байт: Reserved для UDP-endpoint WARP
// (warp-plus читает config.client_id и берёт из него первые три байта).
var reservedClientID = base64.StdEncoding.EncodeToString([]byte{0, 0, 0})

// wgcfIdentity — минимальный срез структуры identity warp-plus (wgcf-identity.json).
// Полное заполнение не нужно: warp-plus использует ключи, адреса и client_id.
type wgcfIdentity struct {
	PrivateKey string             `json:"private_key"`
	Token      string             `json:"token"`
	ID         string             `json:"id"`
	Config     wgcfIdentityConfig `json:"config"`
}

type wgcfIdentityConfig struct {
	Peers     []wgcfPeer    `json:"peers"`
	Interface wgcfInterface `json:"interface"`
	Services  wgcfServices  `json:"services"`
	ClientID  string        `json:"client_id"`
}

type wgcfPeer struct {
	PublicKey string           `json:"public_key"`
	Endpoint  wgcfPeerEndpoint `json:"endpoint"`
}

type wgcfPeerEndpoint struct {
	V4    string `json:"v4"`
	V6    string `json:"v6"`
	Host  string `json:"host"`
	Ports []int  `json:"ports"`
}

type wgcfInterface struct {
	Addresses wgcfAddresses `json:"addresses"`
}

type wgcfAddresses struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

type wgcfServices struct {
	HTTPProxy string `json:"http_proxy"`
}

// IdentityPath — путь к identity внутри cache-dir.
func IdentityPath(cacheDir string) string {
	return filepath.Join(cacheDir, identityDirName, identityFileName)
}

// EnsureIdentity готовит identity warp-plus из аккаунта warpgen, если её ещё нет.
// Без готовой identity warp-plus зависает на регистрации: API Cloudflare из РФ
// часто недоступен напрямую, а таймаута на этом шаге у него нет. Ключи на экран
// не выводятся, файл пишется с правами 0600.
//
// Экспортирована для локального preflight'а (пакет core проверяет её до старта).
func EnsureIdentity(cacheDir, accountPath string) error {
	path := IdentityPath(cacheDir)
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	acct, err := registration.LoadAccount(accountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("нет аккаунта warpgen (%s): сгенерируйте командой `warpgen register` или укажите --account", accountPath)
		}
		return err
	}
	if strings.TrimSpace(acct.IPv4) == "" || strings.TrimSpace(acct.IPv6) == "" {
		return fmt.Errorf("в аккаунте %s нет адресов IPv4/IPv6 — перерегистрируйте (`warpgen register --fresh`)", accountPath)
	}

	data, err := buildIdentity(acct)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// buildIdentity собирает JSON identity из аккаунта warpgen.
func buildIdentity(acct registration.Account) ([]byte, error) {
	id := wgcfIdentity{
		PrivateKey: acct.PrivateKey,
		Token:      acct.Token,
		ID:         acct.ID,
	}
	id.Config.ClientID = reservedClientID
	id.Config.Interface.Addresses.V4 = acct.IPv4
	id.Config.Interface.Addresses.V6 = acct.IPv6
	id.Config.Peers = []wgcfPeer{{PublicKey: acct.PeerPublicKey}}
	return json.MarshalIndent(id, "", "  ")
}
