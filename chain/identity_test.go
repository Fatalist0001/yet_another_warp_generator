package chain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

func testAccount() registration.Account {
	return registration.Account{
		ID:            "device-id",
		Token:         "device-token",
		PrivateKey:    "cHJpdmF0ZS1rZXk=",
		PeerPublicKey: "cGVlci1rZXk=",
		IPv4:          "172.16.0.2",
		IPv6:          "2606:4700::1",
	}
}

func TestEnsureIdentityCreatesFile(t *testing.T) {
	dir := t.TempDir()
	accountPath := filepath.Join(dir, "warpgen-account.json")
	if err := registration.SaveAccount(accountPath, testAccount()); err != nil {
		t.Fatalf("сохранение аккаунта: %v", err)
	}
	cacheDir := filepath.Join(dir, "cache")

	if err := ensureIdentity(cacheDir, accountPath); err != nil {
		t.Fatalf("ensureIdentity: %v", err)
	}

	path := IdentityPath(cacheDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("identity не создана: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("права identity = %o, ожидали 600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got wgcfIdentity
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("identity не разбирается: %v", err)
	}
	acct := testAccount()
	if got.PrivateKey != acct.PrivateKey || got.Token != acct.Token || got.ID != acct.ID {
		t.Error("ключи/токен/ID перенесены неверно")
	}
	if got.Config.Interface.Addresses.V4 != acct.IPv4 || got.Config.Interface.Addresses.V6 != acct.IPv6 {
		t.Error("адреса перенесены неверно")
	}
	if len(got.Config.Peers) != 1 || got.Config.Peers[0].PublicKey != acct.PeerPublicKey {
		t.Error("peer перенесён неверно")
	}
	// client_id должен декодироваться минимум в три байта (Reserved WARP).
	if len(got.Config.ClientID) != 4 {
		t.Errorf("client_id = %q, ожидали 4 символа base64", got.Config.ClientID)
	}
}

func TestEnsureIdentityKeepsExisting(t *testing.T) {
	cacheDir := t.TempDir()
	path := IdentityPath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"private_key":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Аккаунта нет вовсе, но существующая identity должна остаться нетронутой.
	if err := ensureIdentity(cacheDir, filepath.Join(cacheDir, "нет-такого-аккаунта.json")); err != nil {
		t.Fatalf("существующая identity должна приниматься: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"private_key":"x"}` {
		t.Error("существующая identity перезаписана")
	}
}

func TestEnsureIdentityMissingAccount(t *testing.T) {
	dir := t.TempDir()
	err := ensureIdentity(filepath.Join(dir, "cache"), filepath.Join(dir, "нет.json"))
	if err == nil {
		t.Fatal("без аккаунта должна быть ошибка с подсказкой")
	}
}

func TestEnsureIdentityIncompleteAccount(t *testing.T) {
	dir := t.TempDir()
	accountPath := filepath.Join(dir, "account.json")
	acct := testAccount()
	acct.IPv6 = ""
	if err := registration.SaveAccount(accountPath, acct); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(filepath.Join(dir, "cache"), accountPath); err == nil {
		t.Fatal("аккаунт без IPv6 должен быть отвергнут")
	}
}
