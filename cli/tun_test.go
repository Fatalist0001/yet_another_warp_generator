package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateJSON — журнал состояния в формате vpn.saveState.
const stateJSON = `{"steps":[{"argv":["ip","route","del","0.0.0.0/1","dev","warpgen0"],"inverse":["ip","route","replace","0.0.0.0/1","dev","warpgen0"]}]}`

// writeTunState кладёт журнал в $HOME/.warpgen/state (HOME в тесте — temp).
func writeTunState(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".warpgen", "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "warpgen0.st")
	if err := os.WriteFile(path, []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tun-revert без файла состояния — понятная ошибка, ничего не выполняется.
func TestRunTunRevertNoFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runTunRevert(nil)
	if err == nil || !strings.Contains(err.Error(), "файла состояния") {
		t.Errorf("err = %v", err)
	}
}

// dry-run печатает команды отката, не выполняя их; журнал остаётся на месте.
func TestRunTunRevertDryRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeTunState(t)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	if err := runTunRevert([]string{"--dry-run"}); err != nil {
		t.Fatalf("runTunRevert --dry-run: %v", err)
	}
	_ = w.Close()
	out := make([]byte, 4096)
	n, _ := r.Read(out)
	if !strings.Contains(string(out[:n]), "ip route replace 0.0.0.0/1 dev warpgen0") {
		t.Errorf("в выводе нет команды отката: %q", out[:n])
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("журнал исчез при dry-run: %v", err)
	}
}

// Команда доступна через Run: без файла состояния возвращает понятную ошибку
// (dry-run без сети и без root).
func TestRunTunRevertDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Run([]string{"tun-revert", "--dry-run"}); err == nil {
		t.Error("без файла состояния ожидали ошибку")
	}
}
