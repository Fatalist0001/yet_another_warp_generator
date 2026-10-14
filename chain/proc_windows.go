//go:build windows

package chain

import "os/exec"

// setProcessAttrs на Windows отдельной группы процессов нет: полагаемся на Kill
// самого процесса (подпроцессы завершатся вместе с родителем).
func setProcessAttrs(*exec.Cmd) {}

// terminateProcessGroup штатно завершает процесс (SIGTERM на Windows недоступен).
func terminateProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// killProcessGroup добивает процесс.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
