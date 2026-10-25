package cli

import (
	"errors"
	"testing"
)

func TestResolveDNSAddrs(t *testing.T) {
	old := resolveDNSHost
	resolveDNSHost = func(host string) (string, error) {
		if host == "dns.example" {
			return "203.0.113.5", nil
		}
		return "", errors.New("нет такого хоста")
	}
	defer func() { resolveDNSHost = old }()

	got, err := resolveDNSAddrs([]string{"1.1.1.1", "dns.example", "dns.example:5353"})
	if err != nil {
		t.Fatalf("resolveDNSAddrs: %v", err)
	}
	want := []string{"1.1.1.1", "203.0.113.5", "203.0.113.5:5353"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveDNSAddrsRejectsUnknownHost(t *testing.T) {
	old := resolveDNSHost
	resolveDNSHost = func(string) (string, error) { return "", errors.New("нет") }
	defer func() { resolveDNSHost = old }()

	if _, err := resolveDNSAddrs([]string{"bad.example"}); err == nil {
		t.Fatal("ожидалась ошибка для нерезолвящегося hostname")
	}
}
