package cli

import (
	"context"
	"fmt"
	"os"
	"time"
)

// stopFilePoll — период проверки файла-сигнала остановки.
const stopFilePoll = 250 * time.Millisecond

// watchStopFile вызывает stop, когда появляется файл path.
//
// Нужен GUI с системным туннелем: процесс запущен через pkexec под root, а
// непривилегированный GUI не может послать ему SIGINT (EPERM). На Windows
// сигналы дочернему процессу не доставляются вообще, поэтому тем же способом
// останавливается и обычный userspace-прогон. Файл создаёт GUI; CLI читает
// его и удаляет, чтобы следующий запуск не остановился сразу. watchStopFile
// завершается по ctx или после срабатывания.
func watchStopFile(ctx context.Context, path string, stop func(), interval time.Duration) {
	if path == "" {
		return
	}
	if interval <= 0 {
		interval = stopFilePoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err != nil {
				continue
			}
			_ = os.Remove(path)
			fmt.Fprintf(os.Stderr, "получен файл остановки %s\n", path)
			stop()
			return
		}
	}
}
