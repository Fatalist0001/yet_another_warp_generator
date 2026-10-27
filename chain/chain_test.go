package chain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/registration"
)

// writeAccountFile сохраняет тестовый аккаунт warpgen и возвращает путь к нему.
func writeAccountFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "warpgen-account.json")
	if err := registration.SaveAccount(path, testAccount()); err != nil {
		t.Fatalf("сохранение аккаунта: %v", err)
	}
	return path
}

// Каталог кэша по умолчанию — абсолютный путь в домашнем каталоге: относительный
// создавал бы root-овый каталог в репозитории при запуске `sudo ./warpgen run`
// из корня (он ломал gofmt и обход репозитория).
func TestDefaultCacheDirLivesInHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		// os.UserHomeDir на Windows читает USERPROFILE, а не HOME.
		t.Setenv("USERPROFILE", home)
	}

	want := filepath.Join(home, ".warpgen", "chain")
	if got := DefaultCacheDir(); got != want {
		t.Fatalf("DefaultCacheDir() = %q, ожидали %q", got, want)
	}
	if !filepath.IsAbs(DefaultCacheDir()) {
		t.Error("каталог кэша по умолчанию должен быть абсолютным")
	}
}

func TestRunCountriesStopsOnFirstSuccess(t *testing.T) {
	opts := Options{Countries: []string{"NL", "DE"}, Attempts: 2}
	calls := 0
	try := func(_ context.Context, _ Options, cc string) (*Session, *attemptFailure) {
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
	try := func(context.Context, Options, string) (*Session, *attemptFailure) {
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
	try := func(context.Context, Options, string) (*Session, *attemptFailure) {
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
	if got := readinessReason(&wrongExit{err: errExitRU, got: "RU"}, ""); got != "не та страна (выход RU)" {
		t.Errorf("errExitRU → %q", got)
	}
	if got := readinessReason(&wrongExit{err: errWrongCountry, got: "FR"}, ""); got != "не та страна: выход FR" {
		t.Errorf("errWrongCountry → %q", got)
	}
	if got := readinessReason(&wrongExit{err: errNoGeoService}, ""); got != "геосервисы не ответили — выход не проверен" {
		t.Errorf("errNoGeoService → %q", got)
	}
	if got := readinessReason(errors.New("x"), "waiting on handshake"); got != "нет хендшейка WARP" {
		t.Errorf("прочая ошибка → %q", got)
	}
}

// Ошибка проверки выхода не должна теряться: иначе несовпадение страны выглядит
// как поломка туннеля, и мягкий добор стран никогда не включается.
func TestExitCauseKeepsVerdict(t *testing.T) {
	if exitCause(nil, nil, true) != nil {
		t.Error("без ошибки причина не нужна")
	}
	results := []GeoResult{{Service: "ifconfig.co", IP: "1.2.3.4", Country: "FR"}}
	cause := exitCause(errWrongCountry, results, true)
	if cause == nil || !errors.Is(cause, errWrongCountry) || cause.got != "FR" {
		t.Fatalf("exitCause = %#v", cause)
	}
	// Прокси слушает, а геосервисы молчат — это вердикт «выход не проверен».
	if got := exitCause(errNoGeoService, nil, true); got == nil || !errors.Is(got, errNoGeoService) || got.got != "" {
		t.Errorf("молчание геосервисов при живом прокси → %#v", got)
	}
	// Прокси не слушает — это ещё не повод, причину даст лог warp-plus.
	if got := exitCause(errNoGeoService, nil, false); got != nil {
		t.Errorf("SOCKS5 не поднялся — вердикта быть не должно: %#v", got)
	}
	if got := exitCause(errors.New("timeout"), results, true); got != nil {
		t.Errorf("посторонняя ошибка не должна превращаться в вердикт: %#v", got)
	}
}

// Мягкий добор: сначала страны по строгому критерию, потом те же страны с
// критерием «любой выход вне РФ». Таблица попыток при этом не теряется.
func TestRunWithFallbackAcceptsForeignExit(t *testing.T) {
	opts := Options{Countries: []string{"NL"}, Attempts: 1, Strict: true}
	calls := 0
	try := func(_ context.Context, o Options, cc string) (*Session, *attemptFailure) {
		calls++
		if o.Strict {
			return nil, &attemptFailure{
				Reason:      "не та страна: выход FR",
				Err:         &wrongExit{err: errWrongCountry, got: "FR"},
				Established: true,
				Country:     "FR",
			}
		}
		return &Session{Country: cc, IP: "1.2.3.4", Bind: "127.0.0.1:1"}, nil
	}
	sess, err := runWithFallback(context.Background(), opts, try)
	if err != nil {
		t.Fatalf("runWithFallback: %v", err)
	}
	if calls != 2 {
		t.Errorf("попыток = %d, ожидали 2 (строгая и мягкая)", calls)
	}
	if len(sess.Attempts) != 1 || !sess.Attempts[0].Established {
		t.Errorf("отчёт попыток потерян: %+v", sess.Attempts)
	}
}

// Без поднявшегося Psiphon мягкий добор бессмысленен: он только удлинил бы
// ожидание.
func TestRunWithFallbackSkipsWhenPsiphonNeverUp(t *testing.T) {
	opts := Options{Countries: []string{"NL"}, Attempts: 1, Strict: true}
	calls := 0
	try := func(context.Context, Options, string) (*Session, *attemptFailure) {
		calls++
		return nil, &attemptFailure{Reason: "Psiphon не построил туннель"}
	}
	if _, err := runWithFallback(context.Background(), opts, try); err == nil {
		t.Fatal("ожидали ошибку")
	}
	if calls != 1 {
		t.Errorf("попыток = %d, ожидали одну", calls)
	}
}

// Выход RU мягким критерием не лечится: второй проход только удлинил бы ожидание.
func TestRunWithFallbackSkipsOnRUExit(t *testing.T) {
	opts := Options{Countries: []string{"NL"}, Attempts: 1, Strict: true}
	calls := 0
	try := func(context.Context, Options, string) (*Session, *attemptFailure) {
		calls++
		return nil, &attemptFailure{
			Reason:  "не та страна (выход RU)",
			Err:     &wrongExit{err: errExitRU, got: "RU"},
			Country: "RU",
		}
	}
	if _, err := runWithFallback(context.Background(), opts, try); err == nil {
		t.Fatal("ожидали ошибку")
	}
	if calls != 1 {
		t.Errorf("попыток = %d, ожидали одну", calls)
	}
}

func TestPsiphonNotices(t *testing.T) {
	log := strings.Join([]string{
		`time=1 level=INFO msg="psiphon mode enabled" country=NL`,
		`time=2 level=DEBUG msg="psiphon core notice" type=ConnectingServer data=map[]`,
		`time=3 level=DEBUG msg="psiphon core notice" type=ConnectingServer data=map[]`,
		`time=4 level=DEBUG msg="psiphon core notice" type=EstablishTunnelTimeout data=map[]`,
	}, "\n")
	got := psiphonNotices(log, 3)
	want := []string{"ConnectingServer", "EstablishTunnelTimeout"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("psiphonNotices = %v, ожидали %v", got, want)
	}
	if got := psiphonNotices(log, 1); len(got) != 1 || got[0] != "EstablishTunnelTimeout" {
		t.Errorf("с лимитом 1 = %v", got)
	}
	if got := psiphonNotices("нет notice'ов", 3); len(got) != 0 {
		t.Errorf("без notice'ов = %v", got)
	}
}

func TestDescribeFailure(t *testing.T) {
	logWithNotices := `level=DEBUG msg="psiphon core notice" type=Tunnels data=map[]`
	got := describeFailure(errors.New("готовность не достигнута"), logWithNotices)
	if !strings.Contains(got, "Tunnels") {
		t.Errorf("к причине не добавлены notice'ы: %q", got)
	}
	// Вердикт проверки выхода точнее лога — notice'ы к нему не примешиваем.
	if got := describeFailure(&wrongExit{err: errWrongCountry, got: "FR"}, logWithNotices); got != "не та страна: выход FR" {
		t.Errorf("вердикт страны изменён: %q", got)
	}
}

// Сброс перед повтором: кэш Psiphon удаляется, identity warp-plus остаётся — без
// неё warp-plus уйдёт в регистрацию, которой из РФ может не быть.
func TestResetPsiphonStateKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureIdentity(dir, writeAccountFile(t, dir)); err != nil {
		t.Fatalf("EnsureIdentity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server_list_compressed"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "psiphon"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := resetPsiphonState(dir); err != nil {
		t.Fatalf("resetPsiphonState: %v", err)
	}
	if _, err := os.Stat(IdentityPath(dir)); err != nil {
		t.Errorf("identity должна остаться: %v", err)
	}
	for _, name := range []string{"server_list_compressed", "psiphon"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s должно быть удалено (err=%v)", name, err)
		}
	}
	// Отсутствующий каталог — не ошибка.
	if err := resetPsiphonState(filepath.Join(dir, "нет-такого")); err != nil {
		t.Errorf("нет каталога → %v", err)
	}
}

// Неизвестный код страны warp-plus не примет: его --country — enum, и процесс
// падает до сети. Проверяем это сами, чтобы причина была понятной.
func TestCheckCountriesRejectsUnknown(t *testing.T) {
	if err := checkCountries(Options{Countries: DefaultCountries}); err != nil {
		t.Errorf("страны по умолчанию должны проходить: %v", err)
	}
	err := checkCountries(Options{Countries: []string{"NL", "ru"}})
	if err == nil || !strings.Contains(err.Error(), "RU") {
		t.Fatalf("ожидали отказ по RU, получили %v", err)
	}
	if !strings.Contains(err.Error(), "NL") {
		t.Errorf("в сообщении должен быть список допустимых стран: %v", err)
	}
	if err := checkCountries(Options{}); err == nil {
		t.Error("пустой список должен отвергаться")
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
	got, err := ResolveWarpPlus(bin)
	if err != nil {
		t.Fatalf("ResolveWarpPlus: %v", err)
	}
	if got != bin {
		t.Errorf("ResolveWarpPlus = %q, ожидали %q", got, bin)
	}

	if _, err := ResolveWarpPlus(filepath.Join(dir, "нет-такого")); err == nil {
		t.Error("несуществующий путь должен давать ошибку")
	}
	if _, err := ResolveWarpPlus(dir); err == nil {
		t.Error("каталог вместо бинарника должен давать ошибку")
	}
}

func TestOptionsWithDefaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.Endpoint != DefaultEndpoint || got.Bind != DefaultBind || got.CacheDir != DefaultCacheDir() {
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
