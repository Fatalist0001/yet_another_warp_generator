package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchStopFileStopsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tun.stop")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStopFile(ctx, path, func() { close(stopped) }, 5*time.Millisecond)
	}()

	if err := os.WriteFile(path, []byte("stop"), 0o600); err != nil {
		t.Fatalf("создать файл остановки: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("файл остановки не сработал")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchStopFile не завершился после срабатывания")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл остановки не удалён: %v", err)
	}
}

func TestWatchStopFileStopsOnContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStopFile(
			ctx,
			filepath.Join(dir, "tun.stop"),
			func() { close(stopped) },
			5*time.Millisecond,
		)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchStopFile не завершился по отмене контекста")
	}
	select {
	case <-stopped:
		t.Fatal("остановка сработала без файла")
	default:
	}
}

func TestWatchStopFileEmptyPath(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStopFile(context.Background(), "", func() {}, time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("пустой путь должен сразу завершать наблюдение")
	}
}
