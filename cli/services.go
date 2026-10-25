package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
	"github.com/Fatalist0001/yet_another_warp_generator/services"
)

// runServices проверяет доступность сервисов через уже работающий SOCKS5.
// Команда не запускает туннель: её вызывают после события ready.
func runServices(args []string) error {
	fs := flag.NewFlagSet("services", flag.ContinueOnError)
	socks := fs.String("socks", "127.0.0.1:8086", "адрес SOCKS5-выхода")
	timeout := fs.Duration("timeout", 8*time.Second, "таймаут одной проверки")
	concurrency := fs.Int("concurrency", 5, "сколько проверок одновременно")
	jsonOut := fs.Bool("json", false, "вывод в JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := chain.NewGeoClient(*socks)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := services.Check(ctx, services.Default, services.Options{
		Client:      client,
		Timeout:     *timeout,
		Concurrency: *concurrency,
	})
	if *jsonOut {
		return printJSON(map[string]any{
			"socks":    *socks,
			"services": results,
		})
	}

	fmt.Printf("Проверка сервисов через %s:\n\n", *socks)
	for _, r := range results {
		mark := "✗"
		if r.OK() {
			mark = "✓"
		}
		line := fmt.Sprintf("  %s %-12s %-12s", mark, r.Label, r.Status)
		if r.Code > 0 {
			line += fmt.Sprintf(" HTTP %d", r.Code)
		}
		if r.RTTMS > 0 {
			line += fmt.Sprintf(" %d мс", r.RTTMS)
		}
		if r.Error != "" {
			line += " — " + r.Error
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	return nil
}
