package chain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCountriesStopsOnFirstSuccess(t *testing.T) {
	opts := Options{Countries: []string{"NL", "DE"}, Attempts: 2}
	calls := 0
	try := func(_ context.Context, cc string) (*Session, *attemptFailure) {
		calls++
		if calls < 3 {
			return nil, &attemptFailure{Reason: "нет хендшейка WARP"}
		}
		return &Session{Country: cc}, nil
	}
	sess, err := runCountries(context.Background(), opts, try)
	if err != nil {
		t.Fatalf("runCountries: %v", err)
	}
	if calls != 3 {
		t.Errorf("попыток = %d, ожидали 3", calls)
	}
	if sess.Country != "DE" {
		t.Errorf("успешная страна = %q, ожидали DE", sess.Country)
	}
	if len(sess.Attempts) != 2 {
		t.Errorf("неудачных попыток = %d, ожидали 2", len(sess.Attempts))
	}
}

func TestRunCountriesCollectsAllAttempts(t *testing.T) {
	opts := Options{Countries: []string{"NL", "FI"}, Attempts: 2}
	try := func(context.Context, string) (*Session, *attemptFailure) {
		return nil, &attemptFailure{Reason: "Psiphon не построил туннель"}
	}
	_, err := runCountries(context.Background(), opts, try)
	var fail *Failure
	if !errors.As(err, &fail) {
		t.Fatalf("ожидали *Failure, получили %v", err)
	}
	if len(fail.Attempts) != 4 {
		t.Errorf("попыток в отчёте = %d, ожидали 4", len(fail.Attempts))
	}
}

func TestRunCountriesHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := Options{Countries: []string{"NL"}, Attempts: 2}
	try := func(context.Context, string) (*Session, *attemptFailure) {
		t.Error("попытка не должна запускаться после отмены контекста")
		return nil, &attemptFailure{Reason: "x"}
	}
	if _, err := runCountries(ctx, opts, try); !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидали context.Canceled, получили %v", err)
	}
}

func TestFormatFailures(t *testing.T) {
	got := FormatFailures([]Attempt{
		{Country: "NL", Number: 1, Reason: "нет хендшейка WARP"},
		{Country: "FI", Number: 2, Reason: "Psiphon не построил туннель"},
	})
	for _, want := range []string{"СТРАНА", "ПРИЧИНА", "NL", "FI", "нет хендшейка WARP", "Psiphon не построил туннель"} {
		if !strings.Contains(got, want) {
			t.Errorf("в таблице нет %q:\n%s", want, got)
		}
	}
}

func TestClassifyLog(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want string
	}{
		{"psiphon timeout", "... clientlib: tunnel establishment timeout ...", "Psiphon не построил туннель"},
		{"gate", `msg="connection test failed"`, "гейт warp-plus (--test-url)"},
		{"handshake", "waiting on handshake\ncontext deadline exceeded", "нет хендшейка WARP"},
		{"пусто", "", "нет данных (лог пуст)"},
		{"прочее", "level=INFO msg=started", "готовность не достигнута"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyLog(c.log); got != c.want {
				t.Errorf("classifyLog = %q, ожидали %q", got, c.want)
			}
		})
	}
}

func TestReadinessReason(t *testing.T) {
	if got := readinessReason(errExitRU, ""); got != "не та страна (выход RU)" {
		t.Errorf("errExitRU → %q", got)
	}
	if got := readinessReason(errWrongCountry, ""); got != "не та страна" {
		t.Errorf("errWrongCountry → %q", got)
	}
	if got := readinessReason(errors.New("x"), "waiting on handshake"); got != "нет хендшейка WARP" {
		t.Errorf("прочая ошибка → %q", got)
	}
}

func TestPickPrimary(t *testing.T) {
	results := []GeoResult{
		{Service: "a", IP: "1.2.3.4", Country: "FR"},
		{Service: "b", IP: "1.2.3.5", Country: "PL"},
	}
	if got := pickPrimary(results, "PL"); got.Country != "PL" {
		t.Errorf("pickPrimary = %+v, ожидали PL", got)
	}
	if got := pickPrimary(results, "NL"); got.Country != "FR" {
		t.Errorf("без совпадения берём первый ответ, получили %+v", got)
	}
	if got := pickPrimary(nil, "NL"); got.IP != "" || got.Country != "" {
		t.Errorf("пустой список → пустой результат, получили %+v", got)
	}
}

func TestResolveWarpPlusExplicit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "warp-plus")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWarpPlus(bin)
	if err != nil {
		t.Fatalf("resolveWarpPlus: %v", err)
	}
	if got != bin {
		t.Errorf("resolveWarpPlus = %q, ожидали %q", got, bin)
	}

	if _, err := resolveWarpPlus(filepath.Join(dir, "нет-такого")); err == nil {
		t.Error("несуществующий путь должен давать ошибку")
	}
	if _, err := resolveWarpPlus(dir); err == nil {
		t.Error("каталог вместо бинарника должен давать ошибку")
	}
}

func TestOptionsWithDefaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.Endpoint != DefaultEndpoint || got.Bind != DefaultBind || got.CacheDir != DefaultCacheDir {
		t.Errorf("дефолты адресов не подставлены: %+v", got)
	}
	if got.TestURL != DefaultTestURL || got.ReadyTimeout != DefaultReadyTimeout || got.Attempts != DefaultAttempts {
		t.Errorf("дефолты проверки не подставлены: %+v", got)
	}
	if len(got.Countries) != len(DefaultCountries) {
		t.Errorf("страны по умолчанию = %v", got.Countries)
	}
	for _, c := range got.Countries {
		if c == "FI" {
			t.Error("FI не должен попадать в список по умолчанию")
		}
	}
	if got.AccountFile == "" {
		t.Error("файл аккаунта по умолчанию не задан")
	}
}

// stopChild завершает процесс (всю группу) чисто — это путь, по которому идёт
// остановка по SIGINT/SIGTERM.
func TestChildStopIsClean(t *testing.T) {
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("нет команды sleep для проверки остановки процесса")
	}
	logPath := filepath.Join(t.TempDir(), "child.log")
	c, err := startChild(bin, []string{"30"}, logPath)
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	if !c.Alive() {
		t.Fatal("сразу после старта процесс должен быть живым")
	}

	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c.Alive() {
		t.Error("после Stop процесс должен быть завершён")
	}
	if d := time.Since(start); d > childStopGrace+2*time.Second {
		t.Errorf("остановка затянулась: %s", d)
	}
	// Повторный Stop (например, из defer после явной остановки) не должен падать.
	if err := c.Stop(); err != nil {
		t.Errorf("повторный Stop: %v", err)
	}
}

func TestChildLogCaptured(t *testing.T) {
	bin, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("нет sh для проверки перехвата вывода")
	}
	logPath := filepath.Join(t.TempDir(), "child.log")
	c, err := startChild(bin, []string{"-c", "echo waiting on handshake"}, logPath)
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	// Ждём, пока короткая команда отработает сама, и только потом завершаем.
	deadline := time.Now().Add(5 * time.Second)
	for c.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !strings.Contains(c.log(), "waiting on handshake") {
		t.Errorf("вывод процесса не попал в лог: %q", c.log())
	}
}
