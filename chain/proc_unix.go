//go:build !windows

package chain

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// setProcessAttrs выделяет дочерний процесс в отдельную группу, чтобы при
// остановке завершить всё дерево (warp-plus → Psiphon), а не только сам процесс.
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup посылает SIGTERM всей группе процесса.
func terminateProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killProcessGroup добивает группу SIGKILL, если штатно не завершилась.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// attachChildJob — на Unix группу уже задал setProcessAttrs, отдельный job не нужен.
func attachChildJob(*exec.Cmd) func() { return nil }

// listWarpPlus перечисляет процессы warp-plus через /proc: имя (comm) — предфильтр,
// путь — readlink /proc/<pid>/exe (без прав на чужой процесс он не читается, такой
// процесс просто пропускается).
func listWarpPlus() ([]staleProc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []staleProc
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // не каталог PID
		}
		base := filepath.Join("/proc", entry.Name())
		comm, err := os.ReadFile(filepath.Join(base, "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "warp-plus" {
			continue
		}
		exe, err := os.Readlink(filepath.Join(base, "exe"))
		if err != nil {
			continue
		}
		out = append(out, staleProc{pid: pid, path: exe})
	}
	return out, nil
}

// killPID завершает процесс SIGKILL.
func killPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// sameBinaryPath — сравнение путей бинарников на POSIX (регистр значим).
func sameBinaryPath(a, b string) bool { return a == b }
