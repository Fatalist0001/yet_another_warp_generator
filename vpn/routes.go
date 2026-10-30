package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Executor — исполнитель системных команд. Все обращения к системе идут через
// него, поэтому RouteManager проверяется на подделке: без root, ip и resolvectl.
type Executor interface {
	// Output выполняет команду и возвращает stdout (только чтение состояния).
	Output(ctx context.Context, name string, args ...string) (string, error)
	// Run выполняет команду, меняющую состояние системы.
	Run(ctx context.Context, name string, args ...string) error
	// LookPath сообщает, есть ли команда в PATH.
	LookPath(name string) bool
}

// Plan — что должен сделать RouteManager.
type Plan struct {
	// TUNName — имя TUN-интерфейса.
	TUNName string
	// TUNAddr / TUNPrefix — адрес и маска TUN.
	TUNAddr   netip.Addr
	TUNPrefix int
	// MTU — MTU TUN.
	MTU int
	// Exclude — адреса внешних endpoint'ов: их /32 идёт через физический шлюз
	// мимо TUN, иначе сокет внешнего туннеля зациклится в наш же TUN.
	Exclude []netip.Addr
	// DNS — адрес, на который перенаправляем системный DNS: должен лежать в
	// подсети TUN, но не совпадать с её адресом — запросы к адресу интерфейса
	// ядро разбирает локально, а запросы к соседнему адресу уходят в устройство
	// и попадают в наш стек, откуда идут резолверу через SOCKS5.
	DNS netip.Addr
}

// ApplyResult — что сделал Apply.
type ApplyResult struct {
	// Commands — сколько системных команд применили (в dry-run — применили бы).
	Commands int
	// Warnings — не критичные проблемы, например отсутствие resolvectl.
	Warnings []string
	// DNSWarn — системный DNS не перенастроен (возможна утечка DNS).
	DNSWarn string
}

// DNSInfo — DNS текущего (физического) подключения. Используется пресетом
// «DNS провайдера»: пользователю не нужно вводить адрес вручную.
type DNSInfo struct {
	// Interface — интерфейс маршрута по умолчанию.
	Interface string `json:"interface,omitempty"`
	// Servers — DNS-серверы интерфейса или /etc/resolv.conf.
	Servers []string `json:"servers"`
	// Warning — предупреждение, например о loopback-заглушке systemd-resolved.
	Warning string `json:"warning,omitempty"`
}

// RouteManager — интерфейс применения и отката системных настроек.
type RouteManager interface {
	// Apply применяет план. При ошибке сам откатывает применённое.
	Apply(ctx context.Context, plan Plan) (ApplyResult, error)
	// SetEndpoint заменяет исключение внешнего endpoint: старый /32 снимается,
	// новый добавляется (оба шага отменяются Revert).
	SetEndpoint(ctx context.Context, ip netip.Addr) error
	// Revert отменяет ровно те команды, что выполнили Apply и SetEndpoint.
	Revert(ctx context.Context) error
}

// step — выполненная команда и её отмена (пустая отмена — отменять нечего).
type step struct {
	argv    []string
	inverse []string
}

// record — накопитель выполненных команд: Revert отменяет их в обратном порядке.
type record struct {
	exec   Executor
	logf   func(format string, args ...any)
	dryRun bool

	steps   []step
	warns   []string
	dnsWarn string
	// stateFile — путь к файлу состояния для аварийного отката. Перезаписывается
	// атомарно после каждой выполненной команды, поэтому kill -9 не оставит
	// применённые изменения без записи: откат выполняется командой
	// `warpgen tun-revert` (см. RevertTunState).
	stateFile string
	// tolerate — считать ошибку отмены безобидной (например, «цели уже нет»
	// после kill -9). nil — любая ошибка отмены настоящая ошибка.
	tolerate func(argv []string, err error) bool
}

// do выполняет команду и запоминает её вместе с отменой. В dry-run только
// печатает: ни одна системная команда не выполняется.
func (r *record) do(ctx context.Context, argv, inverse []string) error {
	if len(argv) == 0 {
		return errors.New("vpn: пустая команда")
	}
	if r.dryRun {
		r.log("%s", strings.Join(argv, " "))
		r.steps = append(r.steps, step{argv: argv, inverse: inverse})
		return nil
	}
	if err := r.exec.Run(ctx, argv[0], argv[1:]...); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	r.steps = append(r.steps, step{argv: argv, inverse: inverse})
	// Команда выполнена — сразу пишем журнал: окно между выполнением и записью
	// измеряется микросекундами, но даже оно закрыто.
	return r.save()
}

// save атомарно записывает шаги в файл состояния. Пустой путь — файл не ведём
// (dry-run или окружение без каталога состояния).
func (r *record) save() error {
	if r.stateFile == "" {
		return nil
	}
	if err := saveState(r.stateFile, r.steps); err != nil {
		return fmt.Errorf("vpn: запись состояния: %w", err)
	}
	return nil
}

// read выполняет команду и возвращает её вывод. Чтение состояния системы ничего
// не меняет, поэтому в dry-run оно тоже нужно: иначе команды будут напечатаны с
// выдуманным шлюзом.
func (r *record) read(ctx context.Context, name string, args ...string) (string, error) {
	return r.exec.Output(ctx, name, args...)
}

// revert отменяет выполненные команды в обратном порядке. Если своих шагов нет
// (процесс после аварии), шаги загружаются из файла состояния.
//
// Файл состояния ведётся как журнал отката: невыполненные отмены остаются в
// нём, полностью откаченное удаляется. Поэтому упавший посреди отката revert
// можно повторить командой `warpgen tun-revert` — она продолжит с места сбоя,
// а не начнёт сначала.
func (r *record) revert(ctx context.Context) error {
	if len(r.steps) == 0 && r.stateFile != "" {
		steps, err := loadState(r.stateFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // файла нет — откатывать нечего
			}
			return err
		}
		r.steps = steps
	}
	var errs []error
	var failed []step
	for i := len(r.steps) - 1; i >= 0; i-- {
		s := r.steps[i]
		if len(s.inverse) == 0 {
			continue // отменять нечего
		}
		if r.dryRun {
			r.log("%s", strings.Join(s.inverse, " "))
			continue
		}
		if err := r.exec.Run(ctx, s.inverse[0], s.inverse[1:]...); err != nil {
			if r.tolerate != nil && r.tolerate(s.inverse, err) {
				r.log("уже отменено: %s", strings.Join(s.inverse, " "))
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", strings.Join(s.inverse, " "), err))
			failed = append([]step{s}, failed...) // сохраняем исходный порядок шагов
		}
	}
	r.steps = nil
	if r.dryRun || r.stateFile == "" {
		return errors.Join(errs...)
	}
	if len(failed) == 0 {
		// Откат удался целиком — журнал больше не нужен.
		if err := os.Remove(r.stateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("vpn: удаление состояния %s: %w", r.stateFile, err))
		}
	} else if err := saveState(r.stateFile, failed); err != nil {
		// Остались невыполненные отмены — записываем их, чтобы повторный запуск
		// доделал работу, а не начал сначала.
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// stateDoc — содержимое файла состояния. Для отката нужны только шаги с их
// отменами: предупреждения и статусы в файл не пишем.
type stateDoc struct {
	Steps []stateStep `json:"steps"`
}

// stateStep — один шаг состояния. Поля export-ные — так их понимает
// encoding/json; наружу структура не выходит.
type stateStep struct {
	Argv    []string `json:"argv"`
	Inverse []string `json:"inverse"`
}

// saveState атомарно записывает шаги в файл состояния: запись во временный файл
// в том же каталоге, сброс на диск и переименование. Читатель никогда не увидит
// полузаписанный файл, даже если питание пропадёт посреди записи.
func saveState(path string, steps []step) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("vpn: каталог состояния %s: %w", dir, err)
	}
	doc := stateDoc{Steps: make([]stateStep, len(steps))}
	for i, s := range steps {
		doc.Steps[i] = stateStep{Argv: s.argv, Inverse: s.inverse}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("vpn: кодирование состояния: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("vpn: временный файл состояния: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("vpn: запись состояния: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("vpn: сброс состояния на диск: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("vpn: закрытие состояния: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("vpn: переименование состояния: %w", err)
	}
	return nil
}

// loadState читает шаги из файла состояния (в исходном порядке).
func loadState(path string) ([]step, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc stateDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("vpn: повреждённый файл состояния %s: %w", path, err)
	}
	steps := make([]step, 0, len(doc.Steps))
	for _, s := range doc.Steps {
		if len(s.Argv) == 0 {
			return nil, fmt.Errorf("vpn: повреждённый файл состояния %s: шаг без команды", path)
		}
		steps = append(steps, step{argv: s.Argv, inverse: s.Inverse})
	}
	return steps, nil
}

// statePath — путь к файлу состояния TUN-интерфейса. dir пуст — берём
// platformStateDir (каталог создаёт saveState при записи).
func statePath(dir, tunName string) (string, error) {
	if tunName == "" || strings.ContainsAny(tunName, "/\\") {
		return "", fmt.Errorf("vpn: имя интерфейса %q непригодно для файла состояния", tunName)
	}
	if dir == "" {
		dir = platformStateDir()
	}
	return filepath.Join(dir, tunName+".st"), nil
}

// tunDNSSubnet — подсеть TUN (198.18.0.0/15). Адреса из неё warpgen отдаёт
// системе как DNS, и в исправном состоянии они есть только на самом TUN.
// Такой адрес на физическом интерфейсе — всегда наш остаток: без TUN на нём
// никто не слушает, и разрешение имён перестаёт работать целиком.
var tunDNSSubnet = netip.MustParsePrefix("198.18.0.0/15")

// tunDNS сообщает, принадлежит ли адрес подсети TUN. Порт (если он есть) не
// учитывается: серверы Windows приходят без него, а Linux может отдать «ip:53».
func tunDNS(server string) bool {
	host := strings.TrimSpace(server)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return tunDNSSubnet.Contains(addr)
}

// foreignDNS возвращает серверы, которые ставил не warpgen. Адреса из подсети
// TUN отбрасываются: восстановить их — значит закрепить поломку, поэтому такой
// сервер никогда не попадает ни в откат, ни в «прежние» настройки интерфейса.
func foreignDNS(servers []string) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		if tunDNS(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *record) warn(msg string) {
	r.warns = append(r.warns, msg)
}

func (r *record) log(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
	}
}
