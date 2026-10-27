//go:build windows

package vpn

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeWinExecutor подменяет PowerShell: чтение отдаёт заранее заданные ответы,
// запись только записывает команды в журнал.
type fakeWinExecutor struct {
	mu      sync.Mutex
	runs    [][]string
	outputs map[string]string
	failOn  string
}

func newFakeWinExecutor() *fakeWinExecutor {
	return &fakeWinExecutor{
		outputs: map[string]string{
			// Маршрут по умолчанию: физический Ethernet через 192.168.1.1.
			"NextHop": `{"InterfaceAlias":"Ethernet","InterfaceIndex":12,"NextHop":"192.168.1.1"}`,
			// Индекс Wintun-адаптера.
			"Get-NetAdapter": "47\n",
			// DNS физических интерфейсов.
			"Get-DnsClientServerAddress": `[{"InterfaceAlias":"Ethernet","InterfaceIndex":12,"ServerAddresses":["192.168.1.1","8.8.8.8"]}]`,
		},
	}
}

func (e *fakeWinExecutor) Output(_ context.Context, name string, args ...string) (string, error) {
	script := name + " " + strings.Join(args, " ")
	for marker, out := range e.outputs {
		if strings.Contains(script, marker) {
			return out, nil
		}
	}
	return "", fmt.Errorf("неожиданное чтение: %s", script)
}

func (e *fakeWinExecutor) Run(_ context.Context, name string, args ...string) error {
	argv := append([]string{name}, args...)
	e.mu.Lock()
	e.runs = append(e.runs, argv)
	fail := e.failOn
	e.mu.Unlock()
	if fail != "" && strings.Contains(strings.Join(argv, " "), fail) {
		return errors.New("команда упала")
	}
	return nil
}

func (e *fakeWinExecutor) LookPath(string) bool { return true }

func (e *fakeWinExecutor) executed() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.runs...)
}

func (e *fakeWinExecutor) joined() string {
	var lines []string
	for _, argv := range e.executed() {
		lines = append(lines, strings.Join(argv, " "))
	}
	return strings.Join(lines, "\n")
}

func winPlan() Plan {
	return Plan{
		TUNName:   "warpgen0",
		TUNAddr:   netip.MustParseAddr("198.18.0.1"),
		TUNPrefix: 15,
		MTU:       1400,
		Exclude:   []netip.Addr{netip.MustParseAddr("162.159.192.1")},
		DNS:       netip.MustParseAddr("198.18.0.2"),
	}
}

// Apply настраивает адрес, MTU, исключение endpoint, две половины default,
// IPv6, DNS физических интерфейсов и TUN.
func TestWindowsApplyCommands(t *testing.T) {
	exec := newFakeWinExecutor()
	dir := t.TempDir()
	rm, err := newRouteManager(Options{Executor: exec, StateDir: dir})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}

	res, err := rm.Apply(context.Background(), winPlan())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Commands == 0 {
		t.Fatal("Apply не выполнил ни одной команды")
	}

	out := exec.joined()
	for _, want := range []string{
		"New-NetIPAddress -InterfaceAlias 'warpgen0' -IPAddress '198.18.0.1' -PrefixLength 15",
		"Set-NetIPInterface -InterfaceAlias 'warpgen0' -AddressFamily IPv4 -NlMtuBytes 1400",
		"New-NetIPAddress -InterfaceAlias 'warpgen0' -IPAddress 'fd00:198:18::1' -PrefixLength 64 -AddressFamily IPv6",
		"New-NetRoute -DestinationPrefix '162.159.192.1/32' -InterfaceIndex 12 -NextHop '192.168.1.1'",
		"New-NetRoute -DestinationPrefix '0.0.0.0/1' -InterfaceIndex 47",
		"New-NetRoute -DestinationPrefix '128.0.0.0/1' -InterfaceIndex 47",
		"New-NetRoute -DestinationPrefix '::/0' -InterfaceIndex 47",
		"Set-DnsClientServerAddress -InterfaceIndex 12 -ServerAddresses '198.18.0.2'",
		"Set-DnsClientServerAddress -InterfaceAlias 'warpgen0' -ServerAddresses '198.18.0.2'",
		"Clear-DnsClientCache",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет команды %q\n---\n%s", want, out)
		}
	}
	if len(res.Warnings) != 0 {
		t.Errorf("неожиданные предупреждения: %v", res.Warnings)
	}

	// Журнал отката лежит в StateDir и содержит применённые шаги.
	state := filepath.Join(dir, "warpgen0.st")
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("нет файла состояния: %v", err)
	}

	assertCleanupExitsZero(t, exec)
}

// assertCleanupExitsZero ловит граблю PowerShell: на любой «объект не найден»
// он возвращает exit 1 даже под `-ErrorAction SilentlyContinue | Out-Null`,
// из-за чего повторный запуск падал на первом же шаге очистки. Поэтому
// best-effort-команды обязаны заканчиваться `; exit 0`, а смешанные скрипты —
// содержать хотя бы один `-ErrorAction Stop` (там выход честный).
func assertCleanupExitsZero(t *testing.T, exec *fakeWinExecutor) {
	t.Helper()
	for _, argv := range exec.executed() {
		cmd := strings.Join(argv, " ")
		if !strings.Contains(cmd, "SilentlyContinue") || strings.Contains(cmd, "-ErrorAction Stop") {
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(cmd), "; exit 0") {
			t.Errorf("best-effort-команда без `; exit 0` (PowerShell вернёт 1 на пустой системе): %s", cmd)
		}
	}
}

// Revert возвращает DNS физических интерфейсов и снимает маршруты; журнал
// удаляется после успешного отката.
func TestWindowsRevertRestoresDNS(t *testing.T) {
	exec := newFakeWinExecutor()
	dir := t.TempDir()
	rm, err := newRouteManager(Options{Executor: exec, StateDir: dir})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}
	if _, err := rm.Apply(context.Background(), winPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Откат должен вернуть провайдерские DNS, а не оставить адрес TUN.
	exec.mu.Lock()
	exec.runs = nil
	exec.mu.Unlock()

	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	out := exec.joined()
	if !strings.Contains(out, "Set-DnsClientServerAddress -InterfaceIndex 12 -ServerAddresses '192.168.1.1','8.8.8.8'") {
		t.Errorf("DNS интерфейса не восстановлен:\n%s", out)
	}
	for _, want := range []string{
		"Remove-NetRoute -DestinationPrefix '0.0.0.0/1' -InterfaceIndex 47",
		"Remove-NetRoute -DestinationPrefix '128.0.0.0/1' -InterfaceIndex 47",
		"Remove-NetRoute -DestinationPrefix '162.159.192.1/32' -InterfaceIndex 12",
		"Remove-NetRoute -DestinationPrefix '::/0' -InterfaceIndex 47",
		"Remove-NetIPAddress -InterfaceAlias 'warpgen0' -IPAddress 'fd00:198:18::1'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет отмены %q\n---\n%s", want, out)
		}
	}
	assertCleanupExitsZero(t, exec)
	if _, err := os.Stat(filepath.Join(dir, "warpgen0.st")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("журнал не удалён после отката: %v", err)
	}
}

// SetEndpoint переносит /32 на новый адрес.
func TestWindowsSetEndpoint(t *testing.T) {
	exec := newFakeWinExecutor()
	rm, err := newRouteManager(Options{Executor: exec, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}
	if _, err := rm.Apply(context.Background(), winPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	exec.mu.Lock()
	exec.runs = nil
	exec.mu.Unlock()

	next := netip.MustParseAddr("162.159.193.7")
	if err := rm.SetEndpoint(context.Background(), next); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	out := exec.joined()
	if !strings.Contains(out, "Remove-NetRoute -DestinationPrefix '162.159.192.1/32'") {
		t.Errorf("старый endpoint не снят:\n%s", out)
	}
	if !strings.Contains(out, "New-NetRoute -DestinationPrefix '162.159.193.7/32' -InterfaceIndex 12 -NextHop '192.168.1.1'") {
		t.Errorf("новый endpoint не добавлен:\n%s", out)
	}
}

// Отказ IPv6 не валит туннель, а становится предупреждением.
func TestWindowsIPv6FailureIsWarning(t *testing.T) {
	exec := newFakeWinExecutor()
	exec.failOn = "::/0"
	rm, err := newRouteManager(Options{Executor: exec, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}

	res, err := rm.Apply(context.Background(), winPlan())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "; "), "IPv6") {
		t.Errorf("нет предупреждения об IPv6: %v", res.Warnings)
	}
	if !strings.Contains(exec.joined(), "New-NetRoute -DestinationPrefix '0.0.0.0/1'") {
		t.Error("маршрут IPv4 не применён после отказа IPv6")
	}
}

// Добавление маршрутов идемпотентно: остаток от оборванного прошлого запуска
// снимается до New-NetRoute. Иначе подъём туннеля падал с реальной ошибкой
// «Instance MSFT_NetRoute already exists» — маршруты живут в ActiveStore до
// перезагрузки, и убитый процесс оставлял их после себя.
func TestWindowsRoutesClearExisting(t *testing.T) {
	exec := newFakeWinExecutor()
	rm, err := newRouteManager(Options{Executor: exec, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}
	if _, err := rm.Apply(context.Background(), winPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, prefix := range []string{"'162.159.192.1/32'", "'::/0'"} {
		var cmd string
		for _, argv := range exec.executed() {
			s := strings.Join(argv, " ")
			if strings.Contains(s, "New-NetRoute -DestinationPrefix "+prefix) {
				cmd = s
				break
			}
		}
		if cmd == "" {
			t.Errorf("нет команды New-NetRoute для %s", prefix)
			continue
		}
		// Снятие — только по префиксу (без -InterfaceIndex/-NextHop): маршрут
		// прошлого запуска мог остаться на другом шлюзе или интерфейсе.
		remove := "Remove-NetRoute -DestinationPrefix " + prefix + " -PolicyStore ActiveStore"
		i, j := strings.Index(cmd, remove), strings.Index(cmd, "New-NetRoute")
		if i < 0 {
			t.Errorf("%s не снимается перед добавлением:\n%s", prefix, cmd)
			continue
		}
		if i > j {
			t.Errorf("%s: снятие идёт после добавления:\n%s", prefix, cmd)
		}
	}

	// Смешанная команда обязана содержать -ErrorAction Stop, а чистая
	// best-effort — заканчиваться `; exit 0`: по этому коду powershell.exe
	// честно возвращает выход (проверено экспериментально).
	assertCleanupExitsZero(t, exec)
}

// Ошибка на критическом шаге откатывает уже применённое и не оставляет журнал.
func TestWindowsApplyRollsBack(t *testing.T) {
	exec := newFakeWinExecutor()
	exec.failOn = "128.0.0.0/1"
	dir := t.TempDir()
	rm, err := newRouteManager(Options{Executor: exec, StateDir: dir})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}

	if _, err := rm.Apply(context.Background(), winPlan()); err == nil {
		t.Fatal("ожидали ошибку применения")
	}
	if !strings.Contains(exec.joined(), "Remove-NetRoute -DestinationPrefix '0.0.0.0/1' -InterfaceIndex 47") {
		t.Error("применённое не откачено")
	}
	if _, err := os.Stat(filepath.Join(dir, "warpgen0.st")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("журнал остался после отката: %v", err)
	}
}

// ReadCurrentDNS берёт серверы физического интерфейса из PowerShell.
func TestWindowsReadCurrentDNS(t *testing.T) {
	exec := newFakeWinExecutor()
	// У ReadCurrentDNS свой скрипт: убираем остальные ответы, чтобы маркер
	// Get-DnsClientServerAddress не перехватил чтение.
	exec.outputs = map[string]string{
		"Servers": `{"Interface":"Ethernet","Servers":["192.168.1.1","8.8.8.8"]}`,
	}

	info, err := ReadCurrentDNS(context.Background(), exec)
	if err != nil {
		t.Fatalf("ReadCurrentDNS: %v", err)
	}
	if info.Interface != "Ethernet" {
		t.Errorf("интерфейс = %q", info.Interface)
	}
	if len(info.Servers) != 2 || info.Servers[0] != "192.168.1.1" {
		t.Errorf("серверы = %v", info.Servers)
	}
}

func TestParsePSDNS(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{`{"InterfaceAlias":"Ethernet","InterfaceIndex":12,"ServerAddresses":["1.1.1.1"]}`, 1},
		{`[{"InterfaceAlias":"Ethernet","InterfaceIndex":12,"ServerAddresses":["1.1.1.1"]},` +
			`{"InterfaceAlias":"Wi-Fi","InterfaceIndex":7,"ServerAddresses":["8.8.8.8"]}]`, 2},
	}
	for _, c := range cases {
		got, err := parsePSDNS(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("%q: получили %d записей", c.in, len(got))
		}
	}
}
