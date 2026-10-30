package chain

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"time"

	"golang.org/x/net/proxy"
)

const (
	// geoRequestTimeout — потолок на один запрос к геосервису через прокси.
	geoRequestTimeout = 6 * time.Second
	// childStopGrace — сколько ждём штатного завершения дочернего процесса.
	childStopGrace = 5 * time.Second
)

// child — запущенный дочерний процесс warp-plus с выводом в лог-файл.
type child struct {
	cmd     *exec.Cmd
	logPath string
	logFile *os.File
	done    chan struct{}
	waitErr error
	// releaseJob — Windows: закрыть job object с KILL_ON_JOB_CLOSE, чтобы дочерний
	// процесс не пережил warpgen (nil на Unix и когда job недоступен).
	releaseJob func()
}

// startChild запускает дочерний процесс, направляя stdout/stderr в лог-файл
// (на экран вывод не попадает — в нём могут быть служебные строки), и выделяет
// его в отдельную группу процессов, чтобы при остановке завершить всё дерево.
func startChild(bin string, args []string, logPath string) (*child, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	setProcessAttrs(cmd)

	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, err
	}
	c := &child{cmd: cmd, logPath: logPath, logFile: f, done: make(chan struct{})}
	c.releaseJob = attachChildJob(cmd)
	go func() {
		c.waitErr = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

// Alive сообщает, работает ли ещё процесс.
func (c *child) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// log читает накопленный вывод процесса (только для внутреннего разбора причины).
func (c *child) log() string {
	data, err := os.ReadFile(c.logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

// Stop корректно завершает процесс (всю группу) и ждёт выхода.
func (c *child) Stop() error {
	// Job закрывается после остановки: если warpgen убьют жёстко и до Stop дело
	// не дойдёт, handle закроет сама ОС — и warp-plus будет завершён вместе с ним.
	if c.releaseJob != nil {
		defer c.releaseJob()
	}
	if c.logFile != nil {
		defer c.logFile.Close()
	}
	if c.cmd.Process == nil {
		return nil
	}
	if c.Alive() {
		_ = terminateProcessGroup(c.cmd)
		select {
		case <-c.done:
		case <-time.After(childStopGrace):
			_ = killProcessGroup(c.cmd)
			<-c.done
		}
	} else {
		<-c.done
	}
	// Завершение по сигналу при остановке — штатный сценарий, не ошибка.
	var exitErr *exec.ExitError
	if c.waitErr != nil && !errors.As(c.waitErr, &exitErr) {
		return c.waitErr
	}
	return nil
}

// NewGeoClient — HTTP-клиент, ходящий через SOCKS5 warp-plus. Имена резолвятся на
// стороне прокси (аналог socks5h), поэтому локальный DNS не задействован.
// Экспортирован для встраивания (пакет core мониторит живую цепочку).
func NewGeoClient(bind string) (*http.Client, error) {
	d, err := proxy.SOCKS5("tcp", bind, nil, proxy.Direct)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5-диалер не поддерживает контекст")
	}
	return &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       cd.DialContext,
		},
	}, nil
}
