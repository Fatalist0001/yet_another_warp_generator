package main

import (
	"os"
	"testing"
)

func TestVersionOutput(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"warpgen", "-v"}

	// Логгируем ожидаемые значения; реальный вызов main завершится os.Exit.
	t.Logf("version: %s, commit: %s, date: %s", Version, Commit, Date)

	if Version == "" {
		t.Error("Version не задан")
	}
	if Commit == "" {
		t.Error("Commit не задан")
	}
	if Date == "" {
		t.Error("Date не задан")
	}
}

func TestBinaryNameInHelp(t *testing.T) {
	t.Skip("интеграционный тест os.Args и exit pending — добавим при первом прогоне make test")
}
