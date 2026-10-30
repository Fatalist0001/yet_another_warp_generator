//go:build windows

package chain

import (
	"os/exec"
	"testing"
	"time"
)

// TestChildJobKillsProcessOnRelease — job object с KILL_ON_JOB_CLOSE завершает
// дочерний процесс, как только warpgen закрыл handle job'а (это и есть момент
// смерти CLI при жёстком завершении: дочерний warp-plus не должен пережить
// родителя и остаться держать порт с файлами кэша).
func TestChildJobKillsProcessOnRelease(t *testing.T) {
	// ping живёт минуту — достаточно для теста, умирает сам, если job не сработает.
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск ping: %v", err)
	}
	release := attachChildJob(cmd)
	if release == nil {
		_ = cmd.Process.Kill()
		t.Skip("job object недоступен в этом окружении")
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	waited := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		waited <- err
	}()

	release() // закрытие job'а должно убить процесс

	select {
	case <-waited:
		// процесс завершён вместе с job'ом
	case <-time.After(5 * time.Second):
		t.Fatal("процесс пережил закрытие job'а")
	}
}
