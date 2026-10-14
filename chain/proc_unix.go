//go:build !windows

package chain

import (
	"os/exec"
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
