// Package registration — регистрация WARP-аккаунта в неофициальном API Cloudflare
// и локальное хранение результата.
//
// Порт логики из github.com/vernette/warpscout (MIT).
package registration

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/curve25519"
)

// DefaultAccountFile — имя файла аккаунта по умолчанию (в каталоге инструмента).
const DefaultAccountFile = "warpgen-account.json"

// Account — зарегистрированный WARP-аккаунт.
type Account struct {
	// ID — идентификатор устройства в Cloudflare.
	ID string `json:"id"`
	// Token — bearer-токен для последующих запросов к API.
	Token string `json:"token"`
	// PrivateKey — приватный ключ WireGuard клиента (base64).
	PrivateKey string `json:"private_key"`
	// PeerPublicKey — публичный ключ сервера Cloudflare (base64).
	PeerPublicKey string `json:"peer_public_key"`
	// IPv4 — адрес интерфейса IPv4, выданный Cloudflare.
	IPv4 string `json:"ipv4,omitempty"`
	// IPv6 — адрес интерфейса IPv6, выданный Cloudflare.
	IPv6 string `json:"ipv6,omitempty"`
}

// Complete сообщает, что аккаунт пригоден для сборки конфига.
func (a Account) Complete() bool {
	return a.PrivateKey != "" && a.PeerPublicKey != ""
}

// GenerateKeypair генерирует пару ключей WireGuard (Curve25519) в base64.
func GenerateKeypair() (privB64, pubB64 string, err error) {
	var priv [32]byte
	if _, err = rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub), nil
}

// LoadAccount читает аккаунт из файла.
func LoadAccount(path string) (Account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Account{}, err
	}
	var a Account
	if err := json.Unmarshal(data, &a); err != nil {
		return Account{}, fmt.Errorf("%s: повреждённый аккаунт: %w", path, err)
	}
	if !a.Complete() {
		return Account{}, fmt.Errorf("%s: незавершённый аккаунт", path)
	}
	return a, nil
}

// SaveAccount записывает аккаунт в файл с правами 0600.
func SaveAccount(path string, a Account) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadOrRotate возвращает существующий аккаунт для ротации. Отсутствие файла —
// это «нечего ротировать» и не ошибка; любой другой сбой возвращается, чтобы
// случайно не перезаписать файл, который просили ротировать.
func LoadOrRotate(path string) (Account, error) {
	a, err := LoadAccount(path)
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, nil
	}
	if err != nil {
		return Account{}, fmt.Errorf("%v; исправьте или удалите %s, либо используйте --fresh", err, path)
	}
	return a, nil
}
