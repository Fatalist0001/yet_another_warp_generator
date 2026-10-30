package chain

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestMain: при WARPGEN_STALE_HELPER=1 копия тестового бинарника превращается в
// «поддельный warp-plus» — процесс с нужным путём, который висит до убийства.
// Так тест проверяет реальную зачистку, не трогая настоящий warp-plus.
func TestMain(m *testing.M) {
	if os.Getenv("WARPGEN_STALE_HELPER") == "1" {
		select {}
	}
	os.Exit(m.Run())
}

// TestKillStaleWarpPlusKillsProcessOnSamePath — остаточный процесс, запущенный по
// пути нашего бинарника, завершается; чужие пути не трогаются (проверяется ниже
// отдельно на чистой функции).
func TestKillStaleWarpPlusKillsProcessOnSamePath(t *testing.T) {
	dir := t.TempDir()
	name := "warp-plus"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	copySelf(t, bin)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "WARPGEN_STALE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск поддельного warp-plus: %v", err)
	}
	kill := func() { _ = cmd.Process.Kill() }
	defer kill()

	// Ждём, пока процесс появится в платформенном списке.
	var found bool
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		procs, err := listWarpPlus()
		if err == nil {
			for _, p := range procs {
				if sameBinaryPath(mustAbs(t, p.path), mustAbs(t, bin)) {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("поддельный warp-plus не появился в списке процессов")
	}

	killed := KillStaleWarpPlus(bin)
	if len(killed) != 1 {
		t.Fatalf("KillStaleWarpPlus = %v, ожидали ровно одного убитого", killed)
	}

	waited := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		waited <- err
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("процесс не завершился после зачистки")
	}
	// Снимаем ожидание завершения, чтобы defer Kill ничего не сделал повторно.
	kill()
}

// TestStaleVictimsFiltersForeignPathsAndSelf — отбор берёт только совпадение с нашим
// бинарником: чужие пути, пустые пути и собственный PID пропускаются.
func TestStaleVictimsFiltersForeignPathsAndSelf(t *testing.T) {
	bin := filepath.Join("dir", "warp-plus")
	self := 42
	procs := []staleProc{
		{pid: 1, path: filepath.Join("dir", "warp-plus")},   // наш — берём
		{pid: 2, path: filepath.Join("other", "warp-plus")}, // чужой путь
		{pid: 3, path: ""}, // без пути
		{pid: self, path: filepath.Join("dir", "warp-plus")},  // мы сами
		{pid: 4, path: filepath.Join("dir", "warp-plus.exe")}, // другое расширение
	}
	got := staleVictims(procs, bin, self)
	if len(got) != 1 || got[0].pid != 1 {
		t.Fatalf("staleVictims = %+v, ожидали только PID 1", got)
	}
}

// TestResetPsiphonStateRetriesWhileBusy — занятый файл не роняет сброс: после
// освобождения повтор проходит (на Windows открытый файл удалить нельзя, на
// POSIX удаляется сразу — обе ветки заканчиваются успехом).
func TestResetPsiphonStateRetriesWhileBusy(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "psiphon", "server_list_compressed")
	if err := os.MkdirAll(filepath.Dir(blocked), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("state")); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond) // дольше первой паузы повтора
		_ = f.Close()
	}()
	defer func() { _ = f.Close() }()

	if err := resetPsiphonState(dir); err != nil {
		t.Fatalf("после освобождения файла сброс должен пройти: %v", err)
	}
	if _, err := os.Stat(blocked); !os.IsNotExist(err) {
		t.Errorf("файл должен быть удалён (err=%v)", err)
	}
}

// copySelf копирует тестовый бинарник в path — так «поддельный warp-plus» имеет
// нужный путь процесса.
func copySelf(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("открыть %s: %v", self, err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("создать %s: %v", path, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatalf("скопировать %s: %v", path, err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("закрыть %s: %v", path, err)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("Abs(%q): %v", p, err)
	}
	return abs
}
