package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.json")
	eps := []Endpoint{
		{Addr: "162.159.192.1:2408", Colo: "HEL", Loc: "RU", RTT: 84 * time.Millisecond, SpeedMbps: 42.5},
		{Addr: "188.114.96.1:500", Colo: "FRA", Loc: "DE", RTT: 120 * time.Millisecond, SpeedMbps: 11.2},
	}
	if err := SaveProfile(path, ProfileWarpOnly, nil, eps); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got, ok, err := LoadProfile(path, ProfileWarpOnly, nil)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if !ok {
		t.Fatal("профиль не найден")
	}
	if len(got) != len(eps) {
		t.Fatalf("endpoint'ов %d, ожидали %d", len(got), len(eps))
	}
	for i := range eps {
		if got[i].Addr != eps[i].Addr || got[i].Colo != eps[i].Colo || got[i].Loc != eps[i].Loc {
			t.Errorf("endpoint %d = %+v, ожидали %+v", i, got[i], eps[i])
		}
		if got[i].RTT != eps[i].RTT {
			t.Errorf("RTT %d = %v, ожидали %v", i, got[i].RTT, eps[i].RTT)
		}
		if got[i].SpeedMbps != eps[i].SpeedMbps {
			t.Errorf("скорость %d = %v, ожидали %v", i, got[i].SpeedMbps, eps[i].SpeedMbps)
		}
	}
}

func TestProfileSeparatesModeAndCountries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.json")
	warp := []Endpoint{{Addr: "162.159.192.1:2408", SpeedMbps: 30}}
	psiphonNL := []Endpoint{{Addr: "162.159.192.2:2408", SpeedMbps: 12}}
	psiphonSE := []Endpoint{{Addr: "162.159.192.3:2408", SpeedMbps: 7}}

	if err := SaveProfile(path, ProfileWarpOnly, nil, warp); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, ProfilePsiphon, []string{"NL"}, psiphonNL); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, ProfilePsiphon, []string{"SE"}, psiphonSE); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		mode      string
		countries []string
		want      string
	}{
		{ProfileWarpOnly, nil, warp[0].Addr},
		{ProfilePsiphon, []string{"NL"}, psiphonNL[0].Addr},
		{ProfilePsiphon, []string{"SE"}, psiphonSE[0].Addr},
	} {
		got, ok, err := LoadProfile(path, tc.mode, tc.countries)
		if err != nil || !ok {
			t.Fatalf("%s/%v: ok=%v err=%v", tc.mode, tc.countries, ok, err)
		}
		if got[0].Addr != tc.want {
			t.Errorf("%s/%v: endpoint %s, ожидали %s", tc.mode, tc.countries, got[0].Addr, tc.want)
		}
	}

	// Другой порядок стран — другая цепочка, поэтому профиль не подходит.
	if _, ok, err := LoadProfile(path, ProfilePsiphon, []string{"SE", "NL"}); err != nil || ok {
		t.Errorf("профиль для другого порядка стран: ok=%v err=%v", ok, err)
	}
}

func TestRemoveProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.json")
	if err := SaveProfile(path, ProfileWarpOnly, nil, []Endpoint{{Addr: "162.159.192.1:2408"}}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProfile(path); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	if _, ok, err := LoadProfile(path, ProfileWarpOnly, nil); err != nil || ok {
		t.Fatalf("после удаления: ok=%v err=%v", ok, err)
	}
	// Повторное удаление — не ошибка.
	if err := RemoveProfile(path); err != nil {
		t.Fatalf("повторное RemoveProfile: %v", err)
	}
}
