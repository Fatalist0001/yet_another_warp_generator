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
// остановке не выполнился. Журнал применённых команд хранится в
// ~/.warpgen/state/<имя TUN>.st и пишется атомарно после каждой системной
// команды, поэтому откат отменяет ровно то, что успело примениться.
func runTunRevert(args []string) error {
	fs := flag.NewFlagSet("tun-revert", flag.ContinueOnError)
	// Справка по флагам — в stdout, как у run: это рабочая команда.
	fs.SetOutput(os.Stdout)
	tunName := fs.String("tun-name", vpn.DefaultTUNName, "имя TUN-интерфейса")
	dryRun := fs.Bool("dry-run", false, "показать команды отката и ничего не менять")
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
		TUNName: *tunName,
		DryRun:  *dryRun,
		Logf:    func(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("dry-run: в файле состояния %d шагов, команды отката выше (ничего не выполнено)\n", n)
		return nil
	}
	fmt.Printf("Откат выполнен: шагов из файла состояния %d\n", n)
	return nil
}
