package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
)

// dnsPresetReport — пресет с необязательным результатом резолва.
type dnsPresetReport struct {
	config.DNSPreset
	ResolvedIP string `json:"resolved_ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

// runDNSPresets печатает список DNS-пресетов. С флагом --resolve дополнительно
// резолвит hostname'ы, чтобы GUI мог показать пользователю реальные IP.
func runDNSPresets(args []string) error {
	fs := flag.NewFlagSet("dns-presets", flag.ContinueOnError)
	resolve := fs.Bool("resolve", false, "резолвить hostname'ы в IP")
	jsonOut := fs.Bool("json", false, "вывод в JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reports := make([]dnsPresetReport, 0, len(config.DNSPresets))
	for _, p := range config.DNSPresets {
		report := dnsPresetReport{DNSPreset: p}
		if *resolve && p.Value != "" {
			report.ResolvedIP, report.Error = resolvePresetValue(p)
		}
		reports = append(reports, report)
	}

	if *jsonOut {
		return printJSON(reports)
	}

	fmt.Println("DNS-пресеты:")
	for _, r := range reports {
		line := fmt.Sprintf("  %-12s %-38s %s", r.ID, r.Label, r.Value)
		if r.ResolvedIP != "" {
			line += " -> " + r.ResolvedIP
		}
		if r.Error != "" {
			line += " (ошибка: " + r.Error + ")"
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	fmt.Printf("\nПо умолчанию: %s\n", strings.Join(config.DefaultDNSValues(), ", "))
	return nil
}

// resolvePresetValue резолвит значение пресета. Для group резолвит каждый
// элемент и возвращает список через запятую.
func resolvePresetValue(p config.DNSPreset) (string, string) {
	var ips []string
	for _, value := range strings.Split(p.Value, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		ip, err := config.ResolveDNS(value)
		if err != nil {
			return "", fmt.Sprintf("%s: %v", value, err)
		}
		ips = append(ips, ip)
	}
	return strings.Join(ips, ","), ""
}
