package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/core"
)

// captureOutput перехватывает stdout и stderr на время работы fn.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { data, _ := io.ReadAll(outR); outCh <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errCh <- string(data) }()

	fn()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outCh, <-errCh
}

// `warpgen run --help` — рабочая команда: справка по флагам, код возврата 0.
func TestRunHelpWorks(t *testing.T) {
	stdout, _ := captureOutput(t, func() {
		if err := runRun([]string{"--help"}); err != nil {
			t.Errorf("run --help: %v", err)
		}
	})
	for _, flag := range []string{"-countries", "-endpoints", "-bind", "-warp-plus", "-cache-dir", "-json", "-listen", "-warp-only"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("в справке нет флага %s:\n%s", flag, stdout)
		}
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	captureOutput(t, func() {
		if err := runRun([]string{"--нет-такого-флага"}); err == nil {
			t.Error("неизвестный флаг должен давать ошибку")
		}
	})
}

// Пустой список стран отвергается до любых проверок и без сети.
func TestRunEmptyCountries(t *testing.T) {
	captureOutput(t, func() {
		err := runRun([]string{"--countries", ""})
		if err == nil {
			t.Fatal("пустой список стран должен давать ошибку")
		}
		if !strings.Contains(err.Error(), "стран") {
			t.Errorf("текст ошибки = %q", err)
		}
	})
}

// В режиме без Psiphon страны не нужны, но аккаунт по-прежнему обязателен: пустой
// список стран доходит до preflight и падает на аккаунте, а не на странах.
func TestRunWarpOnlyNeedsOnlyAccount(t *testing.T) {
	account := filepath.Join(t.TempDir(), "нет-такого-аккаунта.json")
	captureOutput(t, func() {
		err := runRun([]string{"--warp-only", "--countries", "", "--account", account})
		if err == nil {
			t.Fatal("без аккаунта должен быть отказ preflight")
		}
		if !strings.Contains(err.Error(), "аккаунт") {
			t.Errorf("текст ошибки = %q, ожидали жалобу на аккаунт", err)
		}
		if strings.Contains(err.Error(), "стран") {
			t.Errorf("в режиме --warp-only страны не нужны: %q", err)
		}
	})
}

// Нет аккаунта — внятная ошибка preflight (только локальная проверка).
func TestRunMissingAccountFailsPreflight(t *testing.T) {
	account := filepath.Join(t.TempDir(), "нет-такого-аккаунта.json")
	captureOutput(t, func() {
		err := runRun([]string{"--countries", "NL", "--account", account})
		if err == nil {
			t.Fatal("без аккаунта должен быть отказ preflight")
		}
		if !strings.Contains(err.Error(), "аккаунт") {
			t.Errorf("текст ошибки = %q", err)
		}
	})
}

// В JSON-режиме stdout — чистый NDJSON: по одному событию в строке.
func TestRunEventPrinterJSON(t *testing.T) {
	stdout, stderr := captureOutput(t, func() {
		p := newEventPrinter(true)
		p.print(core.Event{
			Kind: core.EventReady, Time: time.Unix(0, 0).UTC(),
			SOCKS: "127.0.0.1:8086", Country: "NL", IP: "203.0.113.9",
		})
		p.hint("127.0.0.1:8086")
	})

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("в stdout %d строк, ожидали одну (событие):\n%s", len(lines), stdout)
	}
	var ev core.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("строка не разбирается как JSON (%v): %s", err, lines[0])
	}
	if ev.Kind != core.EventReady || ev.SOCKS != "127.0.0.1:8086" || ev.Country != "NL" {
		t.Errorf("событие в JSON = %+v", ev)
	}
	// Подсказка в JSON-режиме идёт в stderr, чтобы не ломать поток.
	if !strings.Contains(stderr, "socks5h://127.0.0.1:8086") {
		t.Errorf("подсказка не попала в stderr: %q", stderr)
	}
}

func TestRunEventPrinterHuman(t *testing.T) {
	stdout, _ := captureOutput(t, func() {
		p := newEventPrinter(false)
		p.print(core.Event{Kind: core.EventOuterUp, Time: time.Unix(0, 0).UTC(), Endpoint: "162.159.192.1:2408"})
		p.print(core.Event{Kind: core.EventReady, Time: time.Unix(0, 0).UTC(), SOCKS: "127.0.0.1:8086", Country: "NL", IP: "203.0.113.9"})
		p.hint("127.0.0.1:8086")
	})
	for _, want := range []string{"162.159.192.1:2408", "socks5://127.0.0.1:8086", "Ctrl+C", "00:00:00"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
}
