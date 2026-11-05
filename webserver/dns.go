package webserver

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/dnscheck"
)

const (
	// maxProbeIPsPerHost — сколько A-записей одного имени проверяем. У DNS-
	// сервисов их обычно 1–3; больше — лишние секунды на мёртвые адреса.
	maxProbeIPsPerHost = 4
	// dnsProbeTimeout — таймаут одной UDP-проверки резолвера.
	dnsProbeTimeout = 3 * time.Second
)

// ProbeResult — итог одной проверки резолвера.
type ProbeResult struct {
	OK  bool
	RTT time.Duration
	Err string
}

type dnsCheckReport struct {
	Host    string   `json:"host"`
	IPs     []string `json:"ips"`
	LiveIPs []string `json:"liveIps"`
	DeadIPs []string `json:"deadIps"`
	OK      bool     `json:"ok"`
	RTTMs   *int64   `json:"rttMs"`
	Note    string   `json:"note"`
}

type dnsPresetReport struct {
	ID      string           `json:"id"`
	Label   string           `json:"label"`
	Host    string           `json:"host"`
	IPs     []string         `json:"ips"`
	LiveIPs []string         `json:"liveIps"`
	OK      bool             `json:"ok"`
	RTTMs   *int64           `json:"rttMs"`
	Note    string           `json:"note"`
	Checks  []dnsCheckReport `json:"checks"`
}

type dnsResponse struct {
	Presets []dnsPresetReport `json:"presets"`
}

// buildDNSReport проверяет все DNS-пресеты из config. Это единственный
// источник списка: dev-server.js — эталон, но в боевом сервере список не
// дублируется вручную.
func buildDNSReport(ctx context.Context, resolve4 func(context.Context, string) ([]string, error), probe func(context.Context, string) ProbeResult) []dnsPresetReport {
	out := make([]dnsPresetReport, 0, len(config.DNSPresets))
	for _, preset := range config.DNSPresets {
		values := splitDNSValue(preset.Value)
		if len(values) == 0 {
			// provider/custom — клиентские опции, у сервера их нет.
			continue
		}

		checks := make([]dnsCheckReport, 0, len(values))
		for _, host := range values {
			checks = append(checks, checkDNSHost(ctx, host, resolve4, probe))
		}

		rep := dnsPresetReport{
			ID:      preset.ID,
			Label:   preset.Label,
			Host:    strings.Join(values, ", "),
			IPs:     make([]string, 0),
			LiveIPs: make([]string, 0),
			Checks:  checks,
		}
		var best *int64
		var notes []string
		for _, c := range checks {
			rep.IPs = append(rep.IPs, c.IPs...)
			rep.LiveIPs = append(rep.LiveIPs, c.LiveIPs...)
			if c.RTTMs != nil && (best == nil || *c.RTTMs < *best) {
				v := *c.RTTMs
				best = &v
			}
			if !c.OK {
				notes = append(notes, c.Host+": "+c.Note)
			}
		}
		rep.OK = len(rep.LiveIPs) > 0
		rep.RTTMs = best
		rep.Note = strings.Join(notes, "; ")
		out = append(out, rep)
	}
	return out
}

func checkDNSHost(ctx context.Context, host string, resolve4 func(context.Context, string) ([]string, error), probe func(context.Context, string) ProbeResult) dnsCheckReport {
	rep := dnsCheckReport{
		Host:    host,
		IPs:     make([]string, 0),
		LiveIPs: make([]string, 0),
		DeadIPs: make([]string, 0),
	}

	ips, err := resolve4(ctx, host)
	if err != nil || len(ips) == 0 {
		rep.Note = "не резолвится"
		return rep
	}
	ips = uniqueIPs(ips)
	if len(ips) > maxProbeIPsPerHost {
		ips = ips[:maxProbeIPsPerHost]
	}

	type probeOut struct {
		ip string
		r  ProbeResult
	}
	results := make([]probeOut, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			results[i] = probeOut{ip: ip, r: probe(ctx, ip)}
		}(i, ip)
	}
	wg.Wait()

	// Сначала живые (по возрастанию RTT), затем мёртвые: клиент берёт первый
	// адрес в `DNS =`, и он должен быть отвечающим.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].r.OK != results[j].r.OK {
			return results[i].r.OK
		}
		if results[i].r.OK && results[j].r.OK {
			return results[i].r.RTT < results[j].r.RTT
		}
		return false
	})

	var best *int64
	var errs []string
	for _, item := range results {
		rep.IPs = append(rep.IPs, item.ip)
		if item.r.OK {
			rep.LiveIPs = append(rep.LiveIPs, item.ip)
			ms := item.r.RTT.Milliseconds()
			if ms < 0 {
				ms = 0
			}
			if best == nil || ms < *best {
				v := ms
				best = &v
			}
			continue
		}
		rep.DeadIPs = append(rep.DeadIPs, item.ip)
		if item.r.Err != "" {
			errs = append(errs, item.ip+": "+item.r.Err)
		}
	}
	rep.OK = len(rep.LiveIPs) > 0
	rep.RTTMs = best
	if !rep.OK {
		if len(errs) > 0 {
			rep.Note = errs[0]
		} else {
			rep.Note = "нет ответа"
		}
	}
	return rep
}

func splitDNSValue(value string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func uniqueIPs(ips []string) []string {
	seen := make(map[string]bool, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	return out
}

// defaultResolve4 резолвит hostname в IPv4. Своих DNS-серверов не используем:
// это системный резолвер процесса, как и в CLI.
func defaultResolve4(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []string{v4.String()}, nil
		}
		return nil, fmt.Errorf("%q не IPv4", host)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out, nil
}

// directDialer — обычный UDP-сокет: сервер проверяет резолвер напрямую, без
// туннеля. Через туннель DNS проверяет CLI (`dns-check`), а веб-сервер не
// поднимает userspace-туннель.
type directDialer struct {
	timeout time.Duration
}

func (d directDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: d.timeout}
	return dialer.DialContext(ctx, network, address)
}

func defaultProbeDNS(ctx context.Context, ip string) ProbeResult {
	res := dnscheck.Check(ctx, directDialer{timeout: dnsProbeTimeout},
		dnscheck.Target{Name: ip, IP: ip},
		dnscheck.Options{Timeout: dnsProbeTimeout, Slow: dnscheck.SlowThreshold})
	out := ProbeResult{OK: res.Status.OK(), RTT: res.RTT}
	if res.Err != nil {
		out.Err = res.Err.Error()
	}
	if !out.OK && out.Err == "" {
		out.Err = res.Status.String()
	}
	return out
}
