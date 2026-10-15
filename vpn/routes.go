package vpn

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
	// DNS — адрес, на который перенаправляем системный DNS (адрес TUN: запросы
	// попадают в наш стек и уходят резолверу через SOCKS5).
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
	return nil
}

// read выполняет команду и возвращает её вывод. Чтение состояния системы ничего
// не меняет, поэтому в dry-run оно тоже нужно: иначе команды будут напечатаны с
// выдуманным шлюзом.
func (r *record) read(ctx context.Context, name string, args ...string) (string, error) {
	return r.exec.Output(ctx, name, args...)
}

// revert отменяет выполненные команды в обратном порядке.
func (r *record) revert(ctx context.Context) error {
	var errs []error
	for i := len(r.steps) - 1; i >= 0; i-- {
		s := r.steps[i]
		if len(s.inverse) == 0 {
			continue
		}
		if r.dryRun {
			r.log("%s", strings.Join(s.inverse, " "))
			continue
		}
		if err := r.exec.Run(ctx, s.inverse[0], s.inverse[1:]...); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", strings.Join(s.inverse, " "), err))
		}
	}
	r.steps = nil
	return errors.Join(errs...)
}

func (r *record) warn(msg string) {
	r.warns = append(r.warns, msg)
}

func (r *record) log(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
	}
}
