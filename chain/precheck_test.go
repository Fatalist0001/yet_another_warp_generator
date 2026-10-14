package chain

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckOuterTunnel(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		status  int
		wantErr bool
	}{
		{"warp on, обычный colo", "warp=on\ncolo=HEL\nloc=RU\nip=104.28.0.1\n", 200, false},
		{"warp off", "warp=off\ncolo=HEL\n", 200, true},
		{"нет строки warp", "colo=HEL\nloc=RU\n", 200, true},
		{"исключённый colo", "warp=on\ncolo=DME\n", 200, true},
		{"исключённый colo в нижнем регистре", "warp=on\ncolo=led\n", 200, true},
		{"ошибка статуса", "warp=on\ncolo=HEL\n", 500, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()

			_, err := checkOuterTunnel(context.Background(), srv.Client(), srv.URL)
			if c.wantErr && err == nil {
				t.Fatal("ожидали ошибку, получили nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("неожидали ошибку: %v", err)
			}
		})
	}
}

func TestCheckOuterTunnelUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // сервер выключен — запрос должен упасть

	if _, err := checkOuterTunnel(context.Background(), http.DefaultClient, url); err == nil {
		t.Fatal("недоступный trace должен давать ошибку")
	}
}
