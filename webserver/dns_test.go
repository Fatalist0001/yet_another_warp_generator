package webserver

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildDNSReportGroup(t *testing.T) {
	resolve := func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "dns.comss.one":
			return []string{"0.0.0.0", "1.2.3.4"}, nil
		case "dns.malw.link":
			return nil, errors.New("nxdomain")
		default:
			return []string{"1.2.3.4"}, nil
		}
	}
	probe := func(_ context.Context, ip string) ProbeResult {
		if ip == "1.2.3.4" {
			return ProbeResult{OK: true, RTT: 5 * time.Millisecond}
		}
		return ProbeResult{OK: false, Err: "нет ответа"}
	}

	reports := buildDNSReport(context.Background(), resolve, probe)
	recommended := findPreset(t, reports, "recommended")
	if !recommended.OK {
		t.Fatalf("recommended должен быть жив: %+v", recommended)
	}
	if recommended.LiveIPs[0] != "1.2.3.4" || recommended.IPs[0] != "1.2.3.4" {
		t.Errorf("живой IP не первый: ips=%v live=%v", recommended.IPs, recommended.LiveIPs)
	}
	if !strings.Contains(recommended.Note, "dns.malw.link: не резолвится") {
		t.Errorf("note = %q", recommended.Note)
	}
	if recommended.RTTMs == nil || *recommended.RTTMs != 5 {
		t.Errorf("rttMs = %v", recommended.RTTMs)
	}

	comss := findPreset(t, reports, "comss")
	if !comss.OK || len(comss.LiveIPs) != 1 || len(comss.Checks) != 1 {
		t.Fatalf("comss: %+v", comss)
	}
	if len(comss.Checks[0].DeadIPs) != 1 || comss.Checks[0].DeadIPs[0] != "0.0.0.0" {
		t.Errorf("deadIps = %v", comss.Checks[0].DeadIPs)
	}

	// provider/custom не имеют значения и не должны попадать в отчёт.
	for _, p := range reports {
		if p.ID == "provider" || p.ID == "custom" {
			t.Errorf("в отчёте лишний пресет %q", p.ID)
		}
	}
}

func TestCheckDNSHostLimit(t *testing.T) {
	var probed atomic.Int64
	rep := checkDNSHost(context.Background(), "many.example",
		func(context.Context, string) ([]string, error) {
			return []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5", "6.6.6.6"}, nil
		},
		func(context.Context, string) ProbeResult {
			probed.Add(1)
			return ProbeResult{OK: false, Err: "таймаут"}
		})
	if got := probed.Load(); got != maxProbeIPsPerHost {
		t.Errorf("проверено %d адресов, want %d", got, maxProbeIPsPerHost)
	}
	if len(rep.IPs) != maxProbeIPsPerHost || len(rep.DeadIPs) != maxProbeIPsPerHost {
		t.Errorf("ips=%v dead=%v", rep.IPs, rep.DeadIPs)
	}
	if rep.OK || rep.Note == "" {
		t.Errorf("мёртвый хост: ok=%v note=%q", rep.OK, rep.Note)
	}
}

func TestDefaultResolve4Literal(t *testing.T) {
	ips, err := defaultResolve4(context.Background(), "127.0.0.1")
	if err != nil || len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Fatalf("IPv4 literal: %v, %v", ips, err)
	}
	if _, err := defaultResolve4(context.Background(), "::1"); err == nil {
		t.Error("IPv6 literal должен отклоняться: DNS в WireGuard только IPv4")
	}
}

func TestDNSHelpers(t *testing.T) {
	got := splitDNSValue(" a, ,b , c ")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitDNSValue = %v", got)
	}
	uniq := uniqueIPs([]string{"1.1.1.1", "1.1.1.1", "", "2.2.2.2"})
	if len(uniq) != 2 || uniq[0] != "1.1.1.1" || uniq[1] != "2.2.2.2" {
		t.Errorf("uniqueIPs = %v", uniq)
	}
}
