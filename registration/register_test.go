package registration

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestGenerateKeypair(t *testing.T) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	privRaw, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Fatalf("приватный ключ не base64: %v", err)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("публичный ключ не base64: %v", err)
	}
	if len(privRaw) != 32 || len(pubRaw) != 32 {
		t.Fatalf("длины ключей: priv=%d pub=%d, want 32/32", len(privRaw), len(pubRaw))
	}
	// Публичный ключ должен быть выводим из приватного.
	wantPub, err := curve25519.X25519(privRaw, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("X25519: %v", err)
	}
	if base64.StdEncoding.EncodeToString(wantPub) != pub {
		t.Error("публичный ключ не соответствует приватному")
	}
	// Клэмпинг приватного ключа.
	if privRaw[0]&7 != 0 {
		t.Error("младшие биты приватного ключа не занулены")
	}
	if privRaw[31]&0x80 != 0 || privRaw[31]&0x40 == 0 {
		t.Error("старшие биты приватного ключа не выставлены по клэмпингу")
	}
}

func TestSaveLoadAccountRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "account.json")

	in := Account{
		ID: "id-1", Token: "tok-1",
		PrivateKey: "priv==", PeerPublicKey: "peer==",
		IPv4: "172.16.0.2", IPv6: "2606:4700::1",
	}
	if err := SaveAccount(path, in); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("права файла = %o, want 600", perm)
	}

	out, err := LoadAccount(path)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip: got %+v, want %+v", out, in)
	}
}

func TestLoadAccountMissing(t *testing.T) {
	_, err := LoadAccount(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Errorf("ожидалась ошибка not-exist, получено %v", err)
	}
}

func TestLoadOrRotateMissingIsEmpty(t *testing.T) {
	a, err := LoadOrRotate(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("LoadOrRotate отсутствующего файла должен вернуть пустой аккаунт без ошибки: %v", err)
	}
	if a.Complete() {
		t.Error("ожидался пустой аккаунт")
	}
}

func TestLoadOrRotateCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrRotate(path); err == nil {
		t.Error("повреждённый файл должен возвращать ошибку, чтобы его не перезаписали")
	}
}

func TestParseRegResponse(t *testing.T) {
	body := []byte(`{
		"id":"abc","token":"tok",
		"config":{
			"peers":[{"public_key":"PEERKEY"}],
			"interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700::9"}}
		}
	}`)
	a, err := ParseRegResponse(body, "MYPRIV==")
	if err != nil {
		t.Fatalf("ParseRegResponse: %v", err)
	}
	if a.PrivateKey != "MYPRIV==" || a.PeerPublicKey != "PEERKEY" {
		t.Errorf("ключи разобраны неверно: %+v", a)
	}
	if a.IPv4 != "172.16.0.2" || a.IPv6 != "2606:4700::9" {
		t.Errorf("адреса разобраны неверно: %+v", a)
	}
	if !a.Complete() {
		t.Error("аккаунт должен быть полным")
	}
}

func TestParseRegResponseMissingPeer(t *testing.T) {
	if _, err := ParseRegResponse([]byte(`{"id":"x"}`), "priv"); err == nil {
		t.Error("ожидалась ошибка при отсутствии публичного ключа пира")
	}
}
