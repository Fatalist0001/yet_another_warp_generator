package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// revertTimeout — предел на аварийный откат: команды ip и resolvectl отвечают
// мгновенно, таймаут нужен только чтобы не зависнуть навсегда.
const revertTimeout = 30 * time.Second

// runTunRevert — команда `tun-revert`: откат системных настроек по файлу
// состояния после аварии (kill -9, сбой питания), когда обычный откат при
// остановке не выполнился. Журнал применённых команд хранится в каталоге
// состояния в профиле пользователя (Linux — ~/.warpgen/state, Windows —
// %LOCALAPPDATA%\warpgen\state) и пишется атомарно после каждой системной
// команды, поэтому откат отменяет ровно то, что успело примениться.
//
// --force нужен, когда журнала нет (портативную папку перенесли или удалили):
// тогда снимаются только уверенно свои следы — DNS из подсети TUN на
// интерфейсах и маршруты через warpgen-адаптер.
func runTunRevert(args []string) error {
	fs := flag.NewFlagSet("tun-revert", flag.ContinueOnError)
	// Справка по флагам — в stdout, как у run: это рабочая команда.
	fs.SetOutput(os.Stdout)
	tunName := fs.String("tun-name", vpn.DefaultTUNName, "имя TUN-интерфейса")
	stateDir := fs.String("state-dir", "", "каталог журнала маршрутов (по умолчанию — каталог состояния в профиле)")
	dryRun := fs.Bool("dry-run", false, "показать команды отката и ничего не менять")
	force := fs.Bool("force", false, "снять остатки без журнала: DNS TUN на интерфейсах и маршруты warpgen (Windows)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), revertTimeout)
	defer cancel()

	// Каждую команду печатаем всегда: аварийный откат должен быть виден целиком.
	n, err := vpn.RevertTunState(ctx, vpn.Options{
		TUNName:  *tunName,
		StateDir: *stateDir,
		DryRun:   *dryRun,
		Force:    *force,
		Logf:     func(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("dry-run: шагов отката %d, команды выше (ничего не выполнено)\n", n)
		return nil
	}
	if n == 0 {
		fmt.Println("Откат выполнен: снимать нечего")
		return nil
	}
	fmt.Printf("Откат выполнен: шагов %d\n", n)
	return nil
}
