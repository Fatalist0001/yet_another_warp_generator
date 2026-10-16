package vpn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Запись и чтение состояния: шаги с пустой отменой (ip ... mtu, resolvectl dns)
// обязаны проходить round-trip — откат просто пропускает такие шаги.
func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warpgen0.st")
	steps := []step{
		{argv: []string{"ip", "link", "set", "dev", "warpgen0", "mtu", "1400"}},
		{argv: []string{"resolvectl", "dns", "eth0", "198.18.0.1"}},
		{
			argv:    []string{"ip", "route", "replace", "0.0.0.0/1", "dev", "warpgen0"},
			inverse: []string{"ip", "route", "del", "0.0.0.0/1", "dev", "warpgen0"},
		},
	}
	if err := saveState(path, steps); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(got) != len(steps) {
		t.Fatalf("прочитано %d шагов, ожидали %d", len(got), len(steps))
	}
	for i := range steps {
		if strings.Join(got[i].argv, " ") != strings.Join(steps[i].argv, " ") {
			t.Errorf("шаг %d argv = %v, ожидали %v", i, got[i].argv, steps[i].argv)
		}
		if strings.Join(got[i].inverse, " ") != strings.Join(steps[i].inverse, " ") {
			t.Errorf("шаг %d inverse = %v, ожидали %v", i, got[i].inverse, steps[i].inverse)
		}
	}
	// Атомарная запись: временного файла после неё не остаётся.
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("остался временный файл: %v", err)
	}
}

// Повреждённое содержимое — понятная ошибка, а не паника или пустой журнал.
func TestStateCorrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warpgen0.st")
	if err := os.WriteFile(path, []byte("это не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(path); err == nil || !strings.Contains(err.Error(), "повреждённый") {
		t.Errorf("err = %v", err)
	}
}

// Запись создаёт каталог состояния, даже если его ещё нет.
func TestSaveStateCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state", "warpgen0.st")
	if err := saveState(path, []step{{argv: []string{"true"}, inverse: []string{"true"}}}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл не создан: %v", err)
	}
}

// Путь к состоянию: своё имя файла и никакого выхода из каталога.
func TestStatePath(t *testing.T) {
	dir := t.TempDir()
	p, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	if p != filepath.Join(dir, "warpgen0.st") {
		t.Errorf("путь = %q", p)
	}
	for _, bad := range []string{"", "../evil", "a/b"} {
		if _, err := statePath(dir, bad); err == nil {
			t.Errorf("statePath(%q) должен вернуть ошибку", bad)
		}
	}
}
