package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/vpn"
)

// runDNSCurrent печатает DNS текущего подключения. Нужен пресету «DNS
// провайдера»: GUI не спрашивает адрес у пользователя, а определяет его сам.
func runDNSCurrent(args []string) error {
	fs := flag.NewFlagSet("dns-current", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "вывод в JSON")
	timeout := fs.Duration("timeout", 5*time.Second, "таймаут определения")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	info, err := vpn.ReadCurrentDNS(ctx, nil)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(info)
	}
	fmt.Printf("Интерфейс: %s\n", info.Interface)
	fmt.Printf("DNS: %s\n", strings.Join(info.Servers, ", "))
	if info.Warning != "" {
		fmt.Printf("Предупреждение: %s\n", info.Warning)
	}
	return nil
}
