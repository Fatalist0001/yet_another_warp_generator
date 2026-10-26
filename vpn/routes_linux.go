//go:build linux && !android

package vpn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"net/netip"
)

// newRouteManager — RouteManager для Linux: команды утилит ip и resolvectl, без
// сторонних библиотек.
func newRouteManager(opts Options) (RouteManager, error) {
	if !opts.DryRun {
		if _, err := exec.LookPath("ip"); err != nil {
			return nil, fmt.Errorf("vpn: не найдена утилита ip (пакет iproute2): %w", err)
		}
	}
	exec := opts.Executor
	if exec == nil {
		exec = osExecutor{}
	}
	return &linuxRoutes{
		exec:     exec,
		logf:     opts.Logf,
		dryRun:   opts.DryRun,
		stateDir: opts.StateDir,
	}, nil
}

// linuxRoutes меняет маршруты, адрес TUN, IPv6-политику и системный DNS.
type linuxRoutes struct {
	exec   Executor
	logf   func(format string, args ...any)
	dryRun bool

	rec *record
	// stateDir — каталог файлов состояния для аварийного отката (пусто —
	// ~/.warpgen/state).
	stateDir string
	// gateway / device — физический маршрут по умолчанию: через него трафик к
	// внешнему endpoint идёт мимо TUN.
	gateway string
	device  string

	endpoint    netip.Addr
	hasEndpoint bool
}

// Apply применяет план целиком; при ошибке откатывает применённое.
func (l *linuxRoutes) Apply(ctx context.Context, plan Plan) (ApplyResult, error) {
	if l.rec != nil {
		return ApplyResult{}, errors.New("vpn: маршруты уже применены")
	}
	rec := &record{exec: l.exec, logf: l.logf, dryRun: l.dryRun, tolerate: linuxTolerate}
	if !l.dryRun {
		// Путь к состоянию готовим до первой команды: страховка пишется после
		// каждого шага, и kill -9 посреди Apply тоже оставит журнал отката.
		path, err := statePath(l.stateDir, plan.TUNName)
		if err != nil {
			return ApplyResult{}, err
		}
		rec.stateFile = path
	}
	if err := l.apply(ctx, rec, plan); err != nil {
		if rerr := rec.revert(ctx); rerr != nil {
			return ApplyResult{}, errors.Join(err, rerr)
		}
		return ApplyResult{}, err
	}
	l.rec = rec
	if len(plan.Exclude) > 0 {
		l.endpoint, l.hasEndpoint = plan.Exclude[0], true
	}
	return ApplyResult{Commands: len(rec.steps), Warnings: rec.warns, DNSWarn: rec.dnsWarn}, nil
}

// SetEndpoint заменяет исключение внешнего endpoint.
func (l *linuxRoutes) SetEndpoint(ctx context.Context, ip netip.Addr) error {
	if l.rec == nil {
		return errors.New("vpn: маршруты не применены")
	}
	if l.hasEndpoint && l.endpoint == ip {
		return nil
	}
	if l.hasEndpoint {
		old := l.endpointArgs(l.endpoint.String() + "/32")
		// Снятие старого /32 и постановка нового: обе команды помним, чтобы
		// Revert вернул прежнее состояние.
		argv := append([]string{"ip", "route", "del"}, old...)
		inverse := append([]string{"ip", "route", "replace"}, old...)
		if err := l.rec.do(ctx, argv, inverse); err != nil {
			return err
		}
	}
	if err := l.addEndpoint(ctx, l.rec, ip); err != nil {
		return err
	}
	l.endpoint, l.hasEndpoint = ip, true
	return nil
}

// Revert отменяет всё, что выполнили Apply и SetEndpoint.
func (l *linuxRoutes) Revert(ctx context.Context) error {
	rec := l.rec
	if rec == nil {
		return nil
	}
	l.rec = nil
	l.hasEndpoint = false
	return rec.revert(ctx)
}

// RevertTunState — аварийный откат по файлу состояния (команда `tun-revert`).
// Читает журнал применённых команд и выполняет их отмену в обратном порядке:
// полностью откаченное состояние удаляется, невыполненные отмены остаются в
// файле, чтобы повторный запуск продолжил с места сбоя. Возвращает, сколько
// шагов было в файле. opts.DryRun — только напечатать команды; opts.Executor —
// подмена исполнителя в тестах.
func RevertTunState(ctx context.Context, opts Options) (int, error) {
	opts = opts.withDefaults()
	path, err := statePath(opts.StateDir, opts.TUNName)
	if err != nil {
		return 0, err
	}
	steps, err := loadState(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("vpn: файла состояния нет (%s): туннель не запускался либо откат уже сделан", path)
		}
		return 0, err
	}
	if len(steps) == 0 {
		return 0, fmt.Errorf("vpn: в файле состояния %s нет шагов — откатывать нечего", path)
	}
	exec := opts.Executor
	if exec == nil {
		exec = osExecutor{}
	}
	rec := &record{
		exec:      exec,
		logf:      opts.Logf,
		dryRun:    opts.DryRun,
		stateFile: path,
		steps:     steps,
		tolerate:  linuxTolerate,
	}
	return len(steps), rec.revert(ctx)
}

// linuxTolerate — отмена не нужна, если цели уже нет: после kill -9 TUN
// уничтожается вместе с процессом, ядро само снимает маршруты через него, и
// удалённые маршруты/интерфейсы отменять нечего. Без этого журнала файл
// состояния никогда не очистился бы в самом частом сценарии аварии.
func linuxTolerate(_ []string, err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"Cannot find device",
		"No such device",
		"No such process",       // ip: маршрута уже нет
		"No such link is known", // resolvectl: интерфейса уже нет
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// apply — последовательность шагов: адрес и MTU TUN → физический маршрут →
// исключение endpoint → маршрут по умолчанию через TUN → блокировка IPv6 →
// системный DNS.
func (l *linuxRoutes) apply(ctx context.Context, rec *record, plan Plan) error {
	tun := plan.TUNName
	addr := fmt.Sprintf("%s/%d", plan.TUNAddr, plan.TUNPrefix)

	// Адрес, MTU и подъём интерфейса. MTU отдельной командой: интерфейс создаётся
	// с ним же, но план должен быть самодостаточным.
	if err := rec.do(ctx, []string{"ip", "link", "set", "dev", tun, "mtu", strconv.Itoa(plan.MTU)}, nil); err != nil {
		return err
	}
	if err := rec.do(ctx, []string{"ip", "addr", "replace", addr, "dev", tun}, []string{"ip", "addr", "del", addr, "dev", tun}); err != nil {
		return err
	}
	if err := rec.do(ctx, []string{"ip", "link", "set", "dev", tun, "up"}, []string{"ip", "link", "set", "dev", tun, "down"}); err != nil {
		return err
	}

	// Физический маршрут по умолчанию: нужен для исключения endpoint.
	gw, dev, err := l.defaultRoute(ctx, rec)
	if err != nil {
		return err
	}
	l.gateway, l.device = gw, dev

	for _, ip := range plan.Exclude {
		if err := l.addEndpoint(ctx, rec, ip); err != nil {
			return err
		}
	}

	// Маршрут по умолчанию через TUN двумя половинами: они перекрывают любой
	// существующий default, не удаляя его.
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		argv := []string{"ip", "route", "replace", dst, "dev", tun}
		inverse := []string{"ip", "route", "del", dst, "dev", tun}
		if err := rec.do(ctx, argv, inverse); err != nil {
			return err
		}
	}

	// IPv6 уводим в чёрную дыру: мост работает только с IPv4, и трафик не должен
	// уходить мимо туннеля.
	argv := []string{"ip", "-6", "route", "replace", "blackhole", "default"}
	inverse := []string{"ip", "-6", "route", "del", "blackhole", "default"}
	if err := rec.do(ctx, argv, inverse); err != nil {
		return err
	}

	return l.applyDNS(ctx, rec, plan, dev)
}

// addEndpoint ставит маршрут /32 внешнего endpoint через физический шлюз.
func (l *linuxRoutes) addEndpoint(ctx context.Context, rec *record, ip netip.Addr) error {
	args := l.endpointArgs(ip.String() + "/32")
	argv := append([]string{"ip", "route", "replace"}, args...)
	inverse := append([]string{"ip", "route", "del"}, args...)
	return rec.do(ctx, argv, inverse)
}

// endpointArgs — аргументы маршрута к endpoint (ip route <cmd> <args>).
func (l *linuxRoutes) endpointArgs(target string) []string {
	if l.gateway != "" {
		return []string{target, "via", l.gateway, "dev", l.device}
	}
	return []string{target, "dev", l.device}
}

// defaultRoute читает физический маршрут по умолчанию.
func (l *linuxRoutes) defaultRoute(ctx context.Context, rec *record) (gw, dev string, err error) {
	out, err := rec.read(ctx, "ip", "route", "show", "default")
	if err != nil {
		return "", "", fmt.Errorf("vpn: чтение маршрута по умолчанию: %w", err)
	}
	gw, dev = parseDefaultRoute(out)
	if dev == "" {
		return "", "", errors.New("vpn: не найден физический маршрут по умолчанию — неизвестно, куда вернуть трафик к внешнему endpoint")
	}
	return gw, dev, nil
}

// parseDefaultRoute достаёт шлюз и интерфейс из вывода `ip route show default`.
func parseDefaultRoute(out string) (gw, dev string) {
	line := out
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		switch fields[i] {
		case "via":
			gw = fields[i+1]
		case "dev":
			dev = fields[i+1]
		}
	}
	return gw, dev
}

// applyDNS перенаправляет системный DNS на адрес из подсети TUN. Прежние
// настройки интерфейса запоминаются, чтобы откат вернул именно их: `resolvectl
// revert` стирает и то, что выставил NetworkManager, — после него интерфейс
// остаётся без DNS и сеть работает только после переподключения.
func (l *linuxRoutes) applyDNS(ctx context.Context, rec *record, plan Plan, physDev string) error {
	if !plan.DNS.IsValid() {
		return nil
	}
	if !l.exec.LookPath("resolvectl") {
		// Молча менять /etc/resolv.conf нельзя: systemd-resolved или сторонний
		// клиент перезапишет его обратно, и утечка DNS останется незамеченной.
		msg := "нет утилиты resolvectl: системный DNS не перенастроен на TUN — возможна утечка DNS"
		rec.dnsWarn = msg
		rec.warn(msg)
		return nil
	}
	oldDNS := l.linkDNS(ctx, rec, physDev)
	oldDomains := l.linkDomains(ctx, rec, physDev)

	argv := []string{"resolvectl", "dns", physDev, plan.DNS.String()}
	if err := rec.do(ctx, argv, restoreDNSArgs(physDev, oldDNS)); err != nil {
		return err
	}
	// Домен ~. уводит в этот интерфейс все запросы: без него systemd-resolved
	// может отдать запрос другому линку.
	argv = []string{"resolvectl", "domain", physDev, "~."}
	return rec.do(ctx, argv, restoreDomainArgs(physDev, oldDomains))
}

// linkDNS читает DNS-серверы интерфейса — их возвращаем при откате. Ошибка
// чтения не мешает работе: тогда откат снимет настройку целиком.
func (l *linuxRoutes) linkDNS(ctx context.Context, rec *record, dev string) []string {
	out, err := rec.read(ctx, "resolvectl", "dns", dev)
	if err != nil {
		return nil
	}
	return parseResolvectlList(out)
}

// linkDomains читает домены маршрутизации интерфейса.
func (l *linuxRoutes) linkDomains(ctx context.Context, rec *record, dev string) []string {
	out, err := rec.read(ctx, "resolvectl", "domain", dev)
	if err != nil {
		return nil
	}
	return parseResolvectlList(out)
}

// ReadCurrentDNS определяет DNS текущего подключения: сначала спрашивает
// systemd-resolved для интерфейса маршрута по умолчанию, затем читает
// /etc/resolv.conf. Провайдерские DNS часто приватные (192.168.x.1) и в TUN
// могут быть недоступны — это не ошибка, а предупреждение в интерфейсе.
func ReadCurrentDNS(ctx context.Context, exec Executor) (DNSInfo, error) {
	if exec == nil {
		exec = osExecutor{}
	}
	out, err := exec.Output(ctx, "ip", "route", "show", "default")
	if err != nil {
		return DNSInfo{}, fmt.Errorf("vpn: чтение маршрута по умолчанию: %w", err)
	}
	_, dev := parseDefaultRoute(out)
	info := DNSInfo{Interface: dev}

	if dev != "" && exec.LookPath("resolvectl") {
		if out, err := exec.Output(ctx, "resolvectl", "dns", dev); err == nil {
			info.Servers = parseResolvectlList(out)
		}
	}
	if len(info.Servers) == 0 {
		if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
			info.Servers = parseResolvConf(string(data))
		}
	}
	if len(info.Servers) == 0 {
		return info, errors.New("vpn: не удалось определить DNS провайдера")
	}
	allLoopback := true
	for _, server := range info.Servers {
		host := server
		if h, _, err := net.SplitHostPort(server); err == nil {
			host = h
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			allLoopback = false
			break
		}
	}
	if allLoopback {
		info.Warning = "DNS провайдера указывает на loopback-заглушку (systemd-resolved); в TUN он может не работать"
	}
	return info, nil
}

// parseResolvConf достаёт nameserver'ы из /etc/resolv.conf.
func parseResolvConf(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	return out
}

// parseResolvectlList достаёт список из вывода `resolvectl dns|domain <линк>`
// вида «Link 2 (eth0): 10.18.47.44 ~.»: берём всё после двоеточия.
func parseResolvectlList(out string) []string {
	line := out
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return nil
	}
	var list []string
	for _, f := range strings.Fields(line[i+1:]) {
		if f != "" {
			list = append(list, f)
		}
	}
	return list
}

// restoreDNSArgs — отмена шага с DNS: возвращаем прежние серверы, а если их не
// было (или прочитать не удалось) — снимаем настройку интерфейса.
func restoreDNSArgs(dev string, servers []string) []string {
	if len(servers) == 0 {
		return []string{"resolvectl", "revert", dev}
	}
	return append([]string{"resolvectl", "dns", dev}, servers...)
}

// restoreDomainArgs — отмена шага с доменами: прежний список, а если его не было
// — пустая строка (так resolvectl очищает домены интерфейса, не трогая
// DNS-серверы; их вернёт отмена предыдущего шага).
func restoreDomainArgs(dev string, domains []string) []string {
	if len(domains) == 0 {
		return []string{"resolvectl", "domain", dev, ""}
	}
	return append([]string{"resolvectl", "domain", dev}, domains...)
}

// osExecutor выполняет команды через os/exec.
type osExecutor struct{}

// command собирает команду в отдельной группе процессов. Без этого Ctrl+C
// прилетает всей группе терминала и убивает подкоманду на полпути (например,
// снятие маршрута при остановке прекращалось с «signal: interrupt» — и маршрут
// оставался в системе). Остановку ведёт сам warpgen: по контексту и StopTimeout.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func (osExecutor) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := command(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", commandError(name, err, stderr.String())
	}
	return string(out), nil
}

func (osExecutor) Run(ctx context.Context, name string, args ...string) error {
	cmd := command(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError(name, err, stderr.String())
	}
	return nil
}

func (osExecutor) LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// commandError добавляет к ошибке команды её stderr.
func commandError(name string, err error, stderr string) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("%s: %s: %w", name, msg, err)
	}
	return fmt.Errorf("%s: %w", name, err)
}
