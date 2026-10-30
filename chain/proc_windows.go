//go:build windows

package chain

import (
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setProcessAttrs — отдельной группы процессов на Windows нет; защита от
// переживания родителя — job object в attachChildJob (вызывается после Start).
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

// attachChildJob ставит дочерний процесс warp-plus в job с флагом
// KILL_ON_JOB_CLOSE: когда warpgen завершится — даже жёстко, через
// TerminateProcess, без отработавшей остановки цепочки — ОС закроет handle job'а
// и убьёт всё оставшееся в нём. Без этого на Windows дочерний процесс переживает
// родителя и остаётся висеть: он держит порт SOCKS5 (--bind) и файлы кэша Psiphon,
// из-за чего следующее подключение падает с «порт занят» и «не удалось сбросить
// кэш Psiphon».
//
// Возвращает функцию закрытия job'а (идемпотентную) или nil, если job недоступен
// (например, warpgen сам запущен внутри job'а с ограничениями) — тогда попытка
// продолжается просто без этой защиты.
func attachChildJob(cmd *exec.Cmd) func() {
	if cmd.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil
	}
	// Handle процесса нужен именно для job'а: свой (os.Process) закрывается сам,
	// а чужой PID из снимка процессов открываем с минимальными правами.
	ph, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		// Не смогли назначить — не роняем попытку, просто без защиты.
		_ = windows.CloseHandle(job)
		return nil
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = windows.CloseHandle(job) })
	}
}

// listWarpPlus перечисляет процессы warp-plus через снимок процессов (Toolhelp):
// имя берётся из снимка, полный путь — через QueryFullProcessImageName с правами
// на запрос ограниченной информации (для своих процессов хватает и без админа).
func listWarpPlus() ([]staleProc, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	var out []staleProc
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "warp-plus.exe") {
			pid := int(entry.ProcessID)
			if path := processImagePath(uint32(pid)); path != "" {
				out = append(out, staleProc{pid: pid, path: path})
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break // конец списка
		}
	}
	return out, nil
}

// processImagePath — полный путь образа процесса ("" — нет прав или он умер).
func processImagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	// Путь в Windows ограничен 32768 символам — хватает запасом.
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// killPID завершает процесс через TerminateProcess.
func killPID(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// sameBinaryPath — сравнение путей бинарников на Windows (регистр не значим).
func sameBinaryPath(a, b string) bool {
	return strings.EqualFold(a, b)
}
