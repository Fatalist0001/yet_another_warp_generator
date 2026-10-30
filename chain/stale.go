package chain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// staleProc — работающий процесс warp-plus, найденный при зачистке.
type staleProc struct {
	pid  int
	path string
}

// KillStaleWarpPlus завершает остаточных warp-plus, запущенных бинарником bin,
// и возвращает описания завершённых (пусто — убирать нечего).
//
// Остаточный процесс появляется, если warpgen завершился жёстко (закрыли окно,
// SIGKILL по таймауту) или умер, не успев остановить цепочку: на Windows дочерний
// процесс переживает родителя. Такой сирота держит порт SOCKS5 (--bind), из-за чего
// новый запуск падает с «bind: address already in use», и файлы кэша Psiphon, из-за
// чего `resetPsiphonState` не может их очистить — каждая попытка падает с
// «не удалось сбросить кэш Psiphon».
//
// Убиваются только процессы, запущенные по пути bin: чужие warp-plus (другой
// инструмент, отдельная распаковка) не трогаются.
func KillStaleWarpPlus(bin string) []string {
	if strings.TrimSpace(bin) == "" {
		return nil
	}
	procs, err := listWarpPlus()
	if err != nil {
		return nil
	}
	var killed []string
	for _, p := range staleVictims(procs, bin, os.Getpid()) {
		if err := killPID(p.pid); err != nil {
			continue // процесс уже завершился сам или недоступен по правам
		}
		killed = append(killed, fmt.Sprintf("%s (PID %d)", p.path, p.pid))
	}
	return killed
}

// staleVictims — чистый отбор: берём только совпадение с нашим бинарником,
// чужие пути, пустые пути и собственный PID пропускаем.
func staleVictims(procs []staleProc, bin string, self int) []staleProc {
	want, err := filepath.Abs(bin)
	if err != nil {
		want = bin
	}
	var out []staleProc
	for _, p := range procs {
		if p.pid == self || p.path == "" {
			continue
		}
		got, err := filepath.Abs(p.path)
		if err != nil {
			continue
		}
		if sameBinaryPath(want, got) {
			out = append(out, p)
		}
	}
	return out
}
