package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMeasureSpeedReadsLimitedBody(t *testing.T) {
	const size = 64 << 10
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, size))
	}))
	defer server.Close()

	res, err := measureSpeed(context.Background(), server.Client(), server.URL, size, 2*time.Second)
	if err != nil {
		t.Fatalf("measureSpeed: %v", err)
	}
	if res.Bytes != size {
		t.Errorf("прочитано %d байт, ожидали %d", res.Bytes, size)
	}
	if res.Mbps <= 0 {
		t.Errorf("скорость = %v, ожидали > 0", res.Mbps)
	}
	if res.Elapsed <= 0 {
		t.Errorf("время = %v, ожидали > 0", res.Elapsed)
	}
}

func TestMeasureSpeedRejectsTinyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	if _, err := measureSpeed(context.Background(), server.Client(), server.URL, 64<<10, 2*time.Second); err == nil {
		t.Fatal("ожидали ошибку на слишком коротком ответе")
	}
}

func TestMeasureSpeedRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	if _, err := measureSpeed(context.Background(), server.Client(), server.URL, 64<<10, 2*time.Second); err == nil {
		t.Fatal("ожидали ошибку на статусе 502")
	}
}
