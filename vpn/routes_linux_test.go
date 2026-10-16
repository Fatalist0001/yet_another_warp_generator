//go:build linux

package vpn

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
)

// planFor — типовой план применения маршрутов.
func planFor(endpoints ...string) Plan {
	exclude := make([]netip.Addr, 0, len(endpoints))
	for _, e := range endpoints {
		exclude = append(exclude, netip.MustParseAddr(e))
	}
	return Plan{
		TUNName:   "warpgen0",
		TUNAddr:   netip.MustParseAddr("198.18.0.1"),
		TUNPrefix: 15,
		MTU:       1400,
		Exclude:   exclude,
		DNS:       netip.MustParseAddr("198.18.0.1"),
	}
}

// fakeExecutor — поддельный исполнитель системных команд: запоминает вызовы и
// умеет падать на выбранной команде.
type fakeExecutor struct {
	// defaultRoute — вывод `ip route show default`.
	defaultRoute string
	// failOn — подстрока команды, на которой Run возвращает ошибку.
	failOn string
	// resolvectl — есть ли утилита в PATH.
	resolvectl bool
	// failMsg — текст ошибки на failOn (по умолчанию «команда упала»).
	failMsg string
	// onRun — хук при каждом Run: например, снять снимок файла состояния.
	onRun func(argv []string)

	mu   sync.Mutex
	runs [][]string
	outs [][]string
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		defaultRoute: "default via 192.168.1.1 dev eth0 proto dhcp src 192.168.1.100 metric 100\n",
		resolvectl:   true,
	}
}

func (e *fakeExecutor) Output(_ context.Context, name string, args ...string) (string, error) {
	e.mu.Lock()
	e.outs = append(e.outs, append([]string{name}, args...))
	e.mu.Unlock()
	if name == "ip" && len(args) >= 2 && args[0] == "route" && args[1] == "show" {
		return e.defaultRoute, nil
	}
	return "", fmt.Errorf("неожиданное чтение: %s %s", name, strings.Join(args, " "))
}

func (e *fakeExecutor) Run(_ context.Context, name string, args ...string) error {
	argv := append([]string{name}, args...)
	e.mu.Lock()
	e.runs = append(e.runs, argv)
	hook := e.onRun
	e.mu.Unlock()
	if hook != nil {
		hook(argv)
	}
	if e.failOn != "" && strings.Contains(strings.Join(argv, " "), e.failOn) {
		msg := e.failMsg
		if msg == "" {
			msg = "команда упала"
		}
		return errors.New(msg)
	}
	return nil
}

func (e *fakeExecutor) LookPath(name string) bool {
	return name == "resolvectl" && e.resolvectl
}

func (e *fakeExecutor) executed() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.runs))
	for _, argv := range e.runs {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

func (e *fakeExecutor) reads() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.outs))
	for _, argv := range e.outs {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// commands — ожидаемый набор команд применения плана.
func applyCommands() []string {
	return []string{
		"ip link set dev warpgen0 mtu 1400",
		"ip addr replace 198.18.0.1/15 dev warpgen0",
		"ip link set dev warpgen0 up",
		"ip route replace 162.159.192.1/32 via 192.168.1.1 dev eth0",
		"ip route replace 0.0.0.0/1 dev warpgen0",
		"ip route replace 128.0.0.0/1 dev warpgen0",
		"ip -6 route replace blackhole default",
		"resolvectl dns eth0 198.18.0.1",
		"resolvectl domain eth0 ~.",
	}
}

// revertCommands — ожидаемый откат: обратный порядок, без шагов без отмены.
func revertCommands() []string {
	return []string{
		"resolvectl revert eth0",
		"ip -6 route del blackhole default",
		"ip route del 128.0.0.0/1 dev warpgen0",
		"ip route del 0.0.0.0/1 dev warpgen0",
		"ip route del 162.159.192.1/32 via 192.168.1.1 dev eth0",
		"ip link set dev warpgen0 down",
		"ip addr del 198.18.0.1/15 dev warpgen0",
	}
}

func newTestRoutes(t *testing.T, exec Executor, dryRun bool) RouteManager {
	t.Helper()
	return newTestRoutesDir(t, exec, dryRun, t.TempDir())
}

// newTestRoutesDir — RouteManager с явным каталогом состояния: в тестах он
// временный, чтобы проверка страховки не трогала реальный ~/.warpgen.
func newTestRoutesDir(t *testing.T, exec Executor, dryRun bool, dir string) RouteManager {
	t.Helper()
	rm, err := newRouteManager(Options{Executor: exec, DryRun: dryRun, StateDir: dir})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}
	return rm
}

func equalCommands(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("команд %d, ожидали %d:\nполучено:\n  %s\nожидали:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("команда %d = %q, ожидали %q", i, got[i], want[i])
		}
	}
	return nil
}

// Apply выполняет ровно ожидаемые команды, Revert отменяет ровно их.
func TestRouteManagerApplyAndRevert(t *testing.T) {
	exec := newFakeExecutor()
	rm := newTestRoutes(t, exec, false)

	res, err := rm.Apply(context.Background(), planFor("162.159.192.1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Commands != len(applyCommands()) {
		t.Errorf("Commands = %d, ожидали %d", res.Commands, len(applyCommands()))
	}
	if res.DNSWarn != "" {
		t.Errorf("DNSWarn = %q, ожидали пусто", res.DNSWarn)
	}
	if err := equalCommands(exec.executed(), applyCommands()); err != nil {
		t.Errorf("применение: %v", err)
	}
	if reads := exec.reads(); len(reads) != 1 || reads[0] != "ip route show default" {
		t.Errorf("чтения: %v", reads)
	}

	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if err := equalCommands(exec.executed(), append(applyCommands(), revertCommands()...)); err != nil {
		t.Errorf("откат: %v", err)
	}
	// Повторный Revert ничего не делает.
	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("повторный Revert: %v", err)
	}
	if err := equalCommands(exec.executed(), append(applyCommands(), revertCommands()...)); err != nil {
		t.Errorf("повторный откат: %v", err)
	}
}

// Ошибка посередине откатывает применённое и возвращает ошибку.
func TestRouteManagerRollbackOnError(t *testing.T) {
	exec := newFakeExecutor()
	exec.failOn = "ip route replace 0.0.0.0/1" // падает на пятом шаге
	rm := newTestRoutes(t, exec, false)

	_, err := rm.Apply(context.Background(), planFor("162.159.192.1"))
	if err == nil {
		t.Fatal("ожидали ошибку применения")
	}
	if !strings.Contains(err.Error(), "0.0.0.0/1") {
		t.Errorf("текст ошибки = %q", err)
	}
	// Упавшая команда в откат не попадает, применённые шаги отменяются в обратном
	// порядке: маршрут к endpoint, подъём интерфейса, адрес.
	want := append(append([]string{}, applyCommands()[:5]...),
		"ip route del 162.159.192.1/32 via 192.168.1.1 dev eth0",
		"ip link set dev warpgen0 down",
		"ip addr del 198.18.0.1/15 dev warpgen0",
	)
	if err := equalCommands(exec.executed(), want); err != nil {
		t.Errorf("откат после ошибки: %v", err)
	}
	// Повторный Apply после отката работает.
	exec.failOn = ""
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("повторный Apply: %v", err)
	}
}

// Пустой физический маршрут — понятная ошибка вместо полусостояния.
func TestRouteManagerNoDefaultRoute(t *testing.T) {
	exec := newFakeExecutor()
	exec.defaultRoute = ""
	rm := newTestRoutes(t, exec, false)

	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err == nil {
		t.Fatal("ожидали ошибку: нет маршрута по умолчанию")
	} else if !strings.Contains(err.Error(), "маршрут по умолчанию") {
		t.Errorf("текст ошибки = %q", err)
	}
	// До чтения маршрута успели поднять адрес — он откачен.
	want := append(applyCommands()[:3],
		"ip link set dev warpgen0 down",
		"ip addr del 198.18.0.1/15 dev warpgen0",
	)
	if err := equalCommands(exec.executed(), want); err != nil {
		t.Errorf("откат: %v", err)
	}
}

// Смена внешнего endpoint: старый /32 снимается, новый ставится, Revert
// возвращает прежнее состояние.
func TestRouteManagerSetEndpoint(t *testing.T) {
	exec := newFakeExecutor()
	rm := newTestRoutes(t, exec, false)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := rm.SetEndpoint(context.Background(), netip.MustParseAddr("162.159.193.5")); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	// Повторный вызов с тем же адресом ничего не делает.
	if err := rm.SetEndpoint(context.Background(), netip.MustParseAddr("162.159.193.5")); err != nil {
		t.Fatalf("повторный SetEndpoint: %v", err)
	}

	want := append(applyCommands(),
		"ip route del 162.159.192.1/32 via 192.168.1.1 dev eth0",
		"ip route replace 162.159.193.5/32 via 192.168.1.1 dev eth0",
	)
	if err := equalCommands(exec.executed(), want); err != nil {
		t.Errorf("смена endpoint: %v", err)
	}

	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	// Новый /32 снимается, старый возвращается (откат идёт в обратном порядке).
	want = append(want,
		"ip route del 162.159.193.5/32 via 192.168.1.1 dev eth0",
		"ip route replace 162.159.192.1/32 via 192.168.1.1 dev eth0",
	)
	want = append(want, revertCommands()...)
	if err := equalCommands(exec.executed(), want); err != nil {
		t.Errorf("откат после смены endpoint: %v", err)
	}
}

// dry-run печатает команды и ничего не выполняет.
func TestRouteManagerDryRun(t *testing.T) {
	exec := newFakeExecutor()
	var printed []string
	rm, err := newRouteManager(Options{
		Executor: exec,
		DryRun:   true,
		Logf:     func(format string, args ...any) { printed = append(printed, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("newRouteManager: %v", err)
	}

	res, err := rm.Apply(context.Background(), planFor("162.159.192.1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(exec.executed()) != 0 {
		t.Errorf("dry-run выполнил команды: %v", exec.executed())
	}
	if res.Commands != len(applyCommands()) {
		t.Errorf("Commands = %d, ожидали %d", res.Commands, len(applyCommands()))
	}
	if err := rm.SetEndpoint(context.Background(), netip.MustParseAddr("162.159.193.5")); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	joined := strings.Join(printed, "\n")
	for _, want := range append(applyCommands(), "ip route replace 162.159.193.5/32 via 192.168.1.1 dev eth0") {
		if !strings.Contains(joined, want) {
			t.Errorf("в dry-run не напечатано: %s", want)
		}
	}
	if len(exec.executed()) != 0 {
		t.Errorf("dry-run выполнил команды: %v", exec.executed())
	}
	// Чтение маршрута по умолчанию в dry-run допустимо: оно ничего не меняет.
	if reads := exec.reads(); len(reads) != 1 {
		t.Errorf("чтений %d, ожидали 1", len(reads))
	}
}

// Без resolvectl системный DNS не трогаем, но предупреждаем об утечке.
func TestRouteManagerWithoutResolvectl(t *testing.T) {
	exec := newFakeExecutor()
	exec.resolvectl = false
	rm := newTestRoutes(t, exec, false)

	res, err := rm.Apply(context.Background(), planFor("162.159.192.1"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.DNSWarn == "" {
		t.Error("ожидали предупреждение об утечке DNS")
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "утечка DNS") {
		t.Errorf("Warnings = %v", res.Warnings)
	}
	for _, cmd := range exec.executed() {
		if strings.Contains(cmd, "resolvectl") {
			t.Errorf("resolvectl не найден, но команда выполнена: %s", cmd)
		}
	}
	if got := len(exec.executed()); got != len(applyCommands())-2 {
		t.Errorf("команд %d, ожидали %d", got, len(applyCommands())-2)
	}
}

// Маршрут без шлюза (point-to-point) тоже разбирается.
func TestRouteManagerWithoutGateway(t *testing.T) {
	exec := newFakeExecutor()
	exec.defaultRoute = "default dev ppp0 scope link metric 50\n"
	rm := newTestRoutes(t, exec, false)

	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "ip route replace 162.159.192.1/32 dev ppp0"
	if !strings.Contains(strings.Join(exec.executed(), "\n"), want) {
		t.Errorf("нет маршрута %q в %v", want, exec.executed())
	}
}

func TestParseDefaultRoute(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		gw, dev string
	}{
		{"с шлюзом", "default via 192.168.1.1 dev eth0 proto dhcp metric 100\n", "192.168.1.1", "eth0"},
		{"без шлюза", "default dev ppp0 scope link\n", "", "ppp0"},
		{"вторая строка игнорируется", "default via 10.0.0.1 dev wlan0\n192.168.0.0/24 dev eth0\n", "10.0.0.1", "wlan0"},
		{"пусто", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gw, dev := parseDefaultRoute(c.out)
			if gw != c.gw || dev != c.dev {
				t.Errorf("получили (%q, %q), ожидали (%q, %q)", gw, dev, c.gw, c.dev)
			}
		})
	}
}

// --- аварийная страховка: файл состояния ---

// kill -9 посреди Apply: на момент падающей команды журнал уже содержит
// применённые шаги, а после отката Apply файл удалён.
func TestRouteManagerStateFileDuringApply(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	exec := newFakeExecutor()
	exec.failOn = "ip route replace 0.0.0.0/1"
	var snap []step
	exec.onRun = func(argv []string) {
		if snap != nil || !strings.Contains(strings.Join(argv, " "), "ip route replace 0.0.0.0/1") {
			return
		}
		steps, err := loadState(path)
		if err != nil {
			t.Errorf("чтение состояния на момент падения: %v", err)
			return
		}
		snap = steps
	}
	rm := newTestRoutesDir(t, exec, false, dir)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err == nil {
		t.Fatal("ожидали ошибку применения")
	}
	// До упавшей команды успели примениться четыре шага: mtu, адрес, подъём, /32.
	if len(snap) != 4 {
		t.Fatalf("на момент падения в журнале %d шагов, ожидали 4: %v", len(snap), snap)
	}
	// Apply сам откатился и удалил журнал.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("после отката файл состояния остался: %v", err)
	}
}

// Авария (kill -9): процесс убит, знаний из памяти нет. Команда tun-revert
// откатывает по журналу ровно те команды, что успели примениться.
func TestRevertTunStateAfterCrash(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	exec := newFakeExecutor()
	rm := newTestRoutesDir(t, exec, false, dir)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// «Процесс убит»: новый исполнитель, состояние только в файле.
	exec2 := newFakeExecutor()
	n, err := RevertTunState(context.Background(), Options{
		StateDir: dir,
		TUNName:  "warpgen0",
		Executor: exec2,
	})
	if err != nil {
		t.Fatalf("RevertTunState: %v", err)
	}
	if n != len(applyCommands()) {
		t.Errorf("шагов %d, ожидали %d", n, len(applyCommands()))
	}
	if err := equalCommands(exec2.executed(), revertCommands()); err != nil {
		t.Errorf("откат по журналу: %v", err)
	}
	// Журнал очищен, повтор честно сообщает, что откатывать нечего.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("после отката журнал не удалён: %v", err)
	}
	if _, err := RevertTunState(context.Background(), Options{StateDir: dir, TUNName: "warpgen0", Executor: newFakeExecutor()}); err == nil || !strings.Contains(err.Error(), "файла состояния") {
		t.Errorf("повторный откат: err = %v", err)
	}
}

// Частично неудачный откат: в журнале остаются только невыполненные отмены,
// повтор продолжает с места сбоя, а не начинает сначала.
func TestRevertTunStatePartialFailure(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	exec := newFakeExecutor()
	rm := newTestRoutesDir(t, exec, false, dir)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	exec.failOn = "ip route del 128.0.0.0/1"
	if err := rm.Revert(context.Background()); err == nil {
		t.Fatal("ожидали ошибку отката")
	}
	steps, err := loadState(path)
	if err != nil {
		t.Fatalf("чтение журнала: %v", err)
	}
	// В журнале — исходный шаг с его отменой: упавшая отмена осталась одна.
	if len(steps) != 1 || strings.Join(steps[0].inverse, " ") != "ip route del 128.0.0.0/1 dev warpgen0" {
		t.Fatalf("после частичного отката в журнале %v, ожидали один упавший шаг", steps)
	}

	// Повтор доделывает ровно недоделанное.
	exec2 := newFakeExecutor()
	if _, err := RevertTunState(context.Background(), Options{StateDir: dir, TUNName: "warpgen0", Executor: exec2}); err != nil {
		t.Fatalf("повторный откат: %v", err)
	}
	if err := equalCommands(exec2.executed(), []string{"ip route del 128.0.0.0/1 dev warpgen0"}); err != nil {
		t.Errorf("повтор: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("после доделки журнал не удалён: %v", err)
	}
}

// Интерфейс уже уничтожен (kill -9 убивает TUN, ядро само снимает маршруты
// через него): такие ошибки отмены — не сбой, журнал должен очиститься.
func TestRevertTunStateToleratesGoneDevice(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	exec := newFakeExecutor()
	rm := newTestRoutesDir(t, exec, false, dir)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Все шаги через warpgen0 уже исчезли вместе с интерфейсом.
	exec.failOn = "dev warpgen0"
	exec.failMsg = "RTNETLINK answers: Cannot find device \"warpgen0\""
	if err := rm.Revert(context.Background()); err != nil {
		t.Fatalf("откат с исчезнувшим интерфейсом: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("журнал не удалён: %v", err)
	}
}

// dry-run аварийного отката: команды печатаются, ничего не выполняется,
// журнал не трогается.
func TestRevertTunStateDryRun(t *testing.T) {
	dir := t.TempDir()
	exec := newFakeExecutor()
	rm := newTestRoutesDir(t, exec, false, dir)
	if _, err := rm.Apply(context.Background(), planFor("162.159.192.1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	exec2 := newFakeExecutor()
	var printed []string
	n, err := RevertTunState(context.Background(), Options{
		StateDir: dir,
		TUNName:  "warpgen0",
		Executor: exec2,
		DryRun:   true,
		Logf:     func(format string, args ...any) { printed = append(printed, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("RevertTunState dry-run: %v", err)
	}
	if n != len(applyCommands()) {
		t.Errorf("шагов %d, ожидали %d", n, len(applyCommands()))
	}
	if got := exec2.executed(); len(got) != 0 {
		t.Errorf("dry-run выполнил команды: %v", got)
	}
	joined := strings.Join(printed, "\n")
	for _, want := range revertCommands() {
		if !strings.Contains(joined, want) {
			t.Errorf("в dry-run не напечатано: %s", want)
		}
	}
	if _, err := loadState(mustStatePath(t, dir)); err != nil {
		t.Errorf("журнал исчез при dry-run: %v", err)
	}
}

// Без файла состояния — понятная ошибка, ни одна команда не выполняется.
func TestRevertTunStateNoFile(t *testing.T) {
	exec := newFakeExecutor()
	_, err := RevertTunState(context.Background(), Options{StateDir: t.TempDir(), TUNName: "warpgen0", Executor: exec})
	if err == nil || !strings.Contains(err.Error(), "файла состояния") {
		t.Errorf("err = %v", err)
	}
	if got := exec.executed(); len(got) != 0 {
		t.Errorf("выполнены команды: %v", got)
	}
}

func mustStatePath(t *testing.T, dir string) string {
	t.Helper()
	p, err := statePath(dir, "warpgen0")
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	return p
}
