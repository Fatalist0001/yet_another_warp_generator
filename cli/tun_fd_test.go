//go:build !windows

package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Проверяет передачу fd через SCM_RIGHTS — тот же путь, которым Android
// VpnService отдаёт TUN Go-пайплайну.
func TestReceiveTunFD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tun.sock")

	type result struct {
		file *os.File
		err  error
	}
	done := make(chan result, 1)
	go func() {
		file, err := receiveTunFD(path)
		done <- result{file: file, err: err}
	}()

	var conn *net.UnixConn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		candidate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err == nil {
			conn = candidate
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("unix-сокет не открылся")
	}
	defer conn.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	raw, err := conn.File()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := unix.Sendmsg(int(raw.Fd()), []byte{1}, unix.UnixRights(int(r.Fd())), nil, 0); err != nil {
		t.Fatalf("sendmsg: %v", err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("receiveTunFD: %v", got.err)
		}
		defer got.file.Close()
		info, err := got.file.Stat()
		if err != nil {
			t.Fatalf("полученный fd не работает: %v", err)
		}
		if info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("получили не тот fd: %v", info.Mode())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("receiveTunFD не вернулся")
	}
}
