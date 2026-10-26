//go:build linux && !android

package vpn

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Linux-часть системного туннеля: Windows проверяется в routes_windows_test.go,
// на macOS — utun. Эти тесты проверяют применение команд ip/resolvectl,
// поэтому на других платформах не собираются.

// dry-run: команды печатаются, но ни TUN, ни маршруты не трогаются.
func TestSystemTunnelDryRun(t *testing.T) {
	exec := newFakeExecutor()
	var printed []string
	st, err := New(Options{
		DryRun:   true,
		Executor: exec,
		DNS:      []string{"1.1.1.1:53", "8.8.8.8:53"},
		Logf:     func(format string, args ...any) { printed = append(printed, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	status, err := st.Start(context.Background(), "127.0.0.1:8086", "162.159.192.1:2408")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !status.DryRun {
		t.Error("статус не помечен как dry-run")
	}
	if status.Name != DefaultTUNName || status.MTU != DefaultTUNMTU {
		t.Errorf("статус = %+v", status)
	}
	if status.Routes != len(applyCommands()) {
		t.Errorf("Routes = %d, ожидали %d", status.Routes, len(applyCommands()))
	}
	if status.DNS != "" {
		t.Errorf("в dry-run резолвер не выбирается, а тут %q", status.DNS)
	}
	if len(exec.executed()) != 0 {
		t.Errorf("dry-run выполнил команды: %v", exec.executed())
	}
	if !strings.Contains(strings.Join(printed, "\n"), "dry-run") {
		t.Errorf("нет пометки dry-run в выводе: %v", printed)
	}
	// Остановка в dry-run тоже ничего не выполняет.
	if err := st.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(exec.executed()) != 0 {
		t.Errorf("dry-run выполнил команды при остановке: %v", exec.executed())
	}
}

// Без прав режим --tun не стартует вовсе: понятное сообщение вместо полусостояния.
func TestSystemTunnelRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("тест проверяет путь без прав, а мы root")
	}
	_, err := New(Options{Executor: newFakeExecutor()})
	if err == nil {
		t.Fatal("ожидали ошибку о правах")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("текст ошибки = %q", err)
	}
}

// Проверки параметров до любых системных действий.
func TestSystemTunnelValidation(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"плохой адрес", Options{TUNAddr: "не-адрес", DryRun: true}, "адрес TUN"},
		{"IPv6 в адресе", Options{TUNAddr: "::1", DryRun: true}, "IPv4"},
		{"длинное имя", Options{TUNName: "очень-длинное-имя-интерфейса", DryRun: true}, "имя интерфейса"},
		{"странный MTU", Options{MTU: 100, DryRun: true}, "MTU"},
		{"странная маска", Options{TUNPrefix: 40, DryRun: true}, "маска"},
		{"неизвестная политика UDP", Options{UDP: "proxy", DryRun: true}, "политика UDP"},
		{"плохой DNS", Options{DNS: []string{"не-адрес"}, DryRun: true}, "резолвера"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.opts)
			if err == nil {
				t.Fatal("ожидали ошибку")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("ошибка = %q, ожидали упоминание %q", err, c.want)
			}
		})
	}
}
