//go:build windows

package vpn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"net/netip"
)

// newRouteManager — RouteManager для Windows: маршруты, адрес TUN и DNS через
// командлеты PowerShell (модуль NetTCPIP). netsh и route.exe печатают сообщения
// на языке системы, а по ним пришлось бы разбирать состояние; объекты PowerShell
// одинаковы в любой локали.
func newRouteManager(opts Options) (RouteManager, error) {
	exec := opts.Executor
	if exec == nil {
		exec = osExecutor{}
	}
	if !opts.DryRun && !exec.LookPath("powershell.exe") {
		return nil, errors.New("vpn: не найден powershell.exe: без него нельзя настроить маршруты и DNS")
	}
	return &windowsRoutes{
		exec:     exec,
		logf:     opts.Logf,
		dryRun:   opts.DryRun,
		stateDir: opts.StateDir,
	}, nil
}

// windowsRoutes меняет адрес и MTU TUN, маршруты, блокировку IPv6 и системный
// DNS. Все изменения идут в ActiveStore: после перезагрузки они не остаются.
type windowsRoutes struct {
	exec   Executor
	logf   func(format string, args ...any)
	dryRun bool

	rec *record
	// stateDir — каталог файлов состояния для аварийного отката (пусто —
	// %USERPROFILE%\.warpgen\state).
	stateDir string

	// phys — физический маршрут по умолчанию: через него трафик к внешнему
	// endpoint идёт мимо TUN.
	phys psDefaultRoute
	// tunIndex — индекс Wintun-адаптера, через который идут /1 и IPv6.
	tunIndex int

	endpoint    netip.Addr
	hasEndpoint bool
}

// psDefaultRoute — маршрут по умолчанию физического интерфейса.
type psDefaultRoute struct {
	InterfaceAlias string
	InterfaceIndex int
	NextHop        string
}

// psDNS — DNS-серверы интерфейса.
type psDNS struct {
	InterfaceAlias  string
	InterfaceIndex  int
	ServerAddresses []string
}

// Apply применяет план целиком; при ошибке откатывает применённое.
func (w *windowsRoutes) Apply(ctx context.Context, plan Plan) (ApplyResult, error) {
	if w.rec != nil {
		return ApplyResult{}, errors.New("vpn: маршруты уже применены")
	}
	rec := &record{exec: w.exec, logf: w.logf, dryRun: w.dryRun}
	if !w.dryRun {
		path, err := statePath(w.stateDir, plan.TUNName)
		if err != nil {
			return ApplyResult{}, err
		}
		rec.stateFile = path
	}
	if err := w.apply(ctx, rec, plan); err != nil {
		if rerr := rec.revert(ctx); rerr != nil {
			return ApplyResult{}, errors.Join(err, rerr)
		}
		return ApplyResult{}, err
	}
	w.rec = rec
	if len(plan.Exclude) > 0 {
		w.endpoint, w.hasEndpoint = plan.Exclude[0], true
	}
	return ApplyResult{Commands: len(rec.steps), Warnings: rec.warns, DNSWarn: rec.dnsWarn}, nil
}

// SetEndpoint заменяет исключение внешнего endpoint.
func (w *windowsRoutes) SetEndpoint(ctx context.Context, ip netip.Addr) error {
	if w.rec == nil {
		return errors.New("vpn: маршруты не применены")
	}
	if w.hasEndpoint && w.endpoint == ip {
		return nil
	}
	if w.hasEndpoint {
		if err := w.removeEndpoint(ctx, w.rec, w.endpoint); err != nil {
			return err
		}
	}
	if err := w.addEndpoint(ctx, w.rec, ip); err != nil {
		return err
	}
	w.endpoint, w.hasEndpoint = ip, true
	return nil
}

// Revert отменяет всё, что выполнили Apply и SetEndpoint.
func (w *windowsRoutes) Revert(ctx context.Context) error {
	rec := w.rec
	if rec == nil {
		return nil
	}
	w.rec = nil
	w.hasEndpoint = false
	err := rec.revert(ctx)
	// Кэш DNS держит старые ответы: после возврата серверов его надо сбросить,
	// иначе система продолжит отдавать адреса, полученные через туннель.
	_ = w.exec.Run(ctx, psExe, psArgs(clearDNSCacheScript)...)
	return err
}

// apply — последовательность: адрес/MTU TUN → физический маршрут → исключение
// endpoint → маршрут по умолчанию через TUN → блокировка IPv6 → системный DNS.
func (w *windowsRoutes) apply(ctx context.Context, rec *record, plan Plan) error {
	tun := psQuote(plan.TUNName)

	// Адрес может остаться от прошлого запуска (адаптер Wintun переиспользуется):
	// снимаем его молча, чтобы New-NetIPAddress не падал на дубликате.
	if err := rec.do(ctx, psCmd(fmt.Sprintf(
		"Remove-NetIPAddress -InterfaceAlias %s -AddressFamily IPv4 -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null", tun)), nil); err != nil {
		return err
	}
	if err := rec.do(ctx, psCmd(fmt.Sprintf(
		"New-NetIPAddress -InterfaceAlias %s -IPAddress %s -PrefixLength %d -AddressFamily IPv4 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
		tun, psQuote(plan.TUNAddr.String()), plan.TUNPrefix)), nil); err != nil {
		return err
	}
	// MTU и метрика: через TUN должен идти приоритетный маршрут.
	if err := rec.do(ctx, psCmd(fmt.Sprintf(
		"Set-NetIPInterface -InterfaceAlias %s -AddressFamily IPv4 -NlMtuBytes %d -InterfaceMetric 1 -ErrorAction Stop",
		tun, plan.MTU)), nil); err != nil {
		return err
	}

	// IPv6-адрес нужен, чтобы на TUN можно было повесить маршрут ::/0: без
	// адреса Windows не добавляет on-link default, и IPv6 уходит мимо
	// туннеля. Необязательный шаг: если IPv6 на адаптере выключен, остаёмся
	// с предупреждением, но IPv4-туннель работает.
	ipv6Ready := true
	if err := rec.do(ctx, psCmd(fmt.Sprintf(
		"Remove-NetIPAddress -InterfaceAlias %s -AddressFamily IPv6 -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null",
		tun)), nil); err != nil {
		ipv6Ready = false
	} else if err := rec.do(ctx, psCmd(fmt.Sprintf(
		"New-NetIPAddress -InterfaceAlias %s -IPAddress %s -PrefixLength 64 -AddressFamily IPv6 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
		tun, psQuote(tunIPv6Addr))), psCmd(fmt.Sprintf(
		"Remove-NetIPAddress -InterfaceAlias %s -IPAddress %s -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null",
		tun, psQuote(tunIPv6Addr)))); err != nil {
		ipv6Ready = false
	}

	index, err := w.tunIndexRead(ctx, rec, plan.TUNName)
	if err != nil {
		return err
	}
	w.tunIndex = index

	phys, err := w.defaultRoute(ctx, rec)
	if err != nil {
		return err
	}
	w.phys = phys

	for _, ip := range plan.Exclude {
		if err := w.addEndpoint(ctx, rec, ip); err != nil {
			return err
		}
	}

	// Маршрут по умолчанию через TUN двумя половинами: они перекрывают любой
	// существующий default, не удаляя его.
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		argv := psCmd(fmt.Sprintf(
			"Remove-NetRoute -DestinationPrefix %s -InterfaceIndex %d -Confirm:$false -ErrorAction SilentlyContinue | Out-Null; "+
				"New-NetRoute -DestinationPrefix %s -InterfaceIndex %d -NextHop '0.0.0.0' -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
			psQuote(prefix), index, psQuote(prefix), index))
		inverse := psCmd(fmt.Sprintf(
			"Remove-NetRoute -DestinationPrefix %s -InterfaceIndex %d -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null",
			psQuote(prefix), index))
		if err := rec.do(ctx, argv, inverse); err != nil {
			return err
		}
	}

	// IPv6 уводим в TUN, где мост его отбрасывает: иначе трафик уйдёт мимо
	// туннеля. Отказ не валит туннель, но превращается в предупреждение.
	if ipv6Ready {
		ipv6 := psCmd(fmt.Sprintf(
			"New-NetRoute -DestinationPrefix '::/0' -InterfaceIndex %d -NextHop '::' -RouteMetric 1 -PolicyStore ActiveStore -AddressFamily IPv6 -ErrorAction Stop | Out-Null", index))
		ipv6Inverse := psCmd(fmt.Sprintf(
			"Remove-NetRoute -DestinationPrefix '::/0' -InterfaceIndex %d -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null", index))
		if err := rec.do(ctx, ipv6, ipv6Inverse); err != nil {
			msg := "IPv6 не перенаправлен в TUN: возможна утечка IPv6 — " + err.Error()
			rec.warn(msg)
			rec.log("%s", msg)
		}
	} else {
		msg := "IPv6 не перенаправлен в TUN: не удалось назначить IPv6-адрес адаптеру — возможна утечка IPv6"
		rec.warn(msg)
		rec.log("%s", msg)
	}

	return w.applyDNS(ctx, rec, plan)
}

// tunIndexRead читает индекс Wintun-адаптера.
func (w *windowsRoutes) tunIndexRead(ctx context.Context, rec *record, tun string) (int, error) {
	out, err := rec.read(ctx, psExe, psArgs(fmt.Sprintf(
		"$a = Get-NetAdapter -Name %s -ErrorAction Stop; $a.ifIndex", psQuote(tun)))...)
	if err != nil {
		return 0, fmt.Errorf("vpn: индекс TUN %s: %w", tun, err)
	}
	index, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || index <= 0 {
		return 0, fmt.Errorf("vpn: не удалось прочитать индекс TUN %s (%q)", tun, strings.TrimSpace(out))
	}
	return index, nil
}

// addEndpoint ставит маршрут /32 внешнего endpoint через физический шлюз.
func (w *windowsRoutes) addEndpoint(ctx context.Context, rec *record, ip netip.Addr) error {
	prefix := ip.String() + "/32"
	argv := psCmd(fmt.Sprintf(
		"New-NetRoute -DestinationPrefix %s -InterfaceIndex %d -NextHop %s -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
		psQuote(prefix), w.phys.InterfaceIndex, psQuote(w.nexthop())))
	inverse := psCmd(fmt.Sprintf(
		"Remove-NetRoute -DestinationPrefix %s -InterfaceIndex %d -NextHop %s -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null",
		psQuote(prefix), w.phys.InterfaceIndex, psQuote(w.nexthop())))
	return rec.do(ctx, argv, inverse)
}

// removeEndpoint снимает маршрут /32 прежнего endpoint; отмена возвращает его.
func (w *windowsRoutes) removeEndpoint(ctx context.Context, rec *record, ip netip.Addr) error {
	prefix := ip.String() + "/32"
	argv := psCmd(fmt.Sprintf(
		"Remove-NetRoute -DestinationPrefix %s -InterfaceIndex %d -NextHop %s -PolicyStore ActiveStore -Confirm:$false -ErrorAction SilentlyContinue | Out-Null",
		psQuote(prefix), w.phys.InterfaceIndex, psQuote(w.nexthop())))
	inverse := psCmd(fmt.Sprintf(
		"New-NetRoute -DestinationPrefix %s -InterfaceIndex %d -NextHop %s -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
		psQuote(prefix), w.phys.InterfaceIndex, psQuote(w.nexthop())))
	return rec.do(ctx, argv, inverse)
}

// nexthop — шлюз физического маршрута; для on-link default это 0.0.0.0.
func (w *windowsRoutes) nexthop() string {
	gw := strings.TrimSpace(w.phys.NextHop)
	if gw == "" {
		return "0.0.0.0"
	}
	return gw
}

// defaultRoute читает физический маршрут по умолчанию.
func (w *windowsRoutes) defaultRoute(ctx context.Context, rec *record) (psDefaultRoute, error) {
	out, err := rec.read(ctx, psExe, psArgs(defaultRouteScript)...)
	if err != nil {
		return psDefaultRoute{}, fmt.Errorf("vpn: чтение маршрута по умолчанию: %w", err)
	}
	var route psDefaultRoute
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &route); err != nil {
		return psDefaultRoute{}, fmt.Errorf("vpn: разбор маршрута по умолчанию: %w", err)
	}
	if route.InterfaceIndex <= 0 {
		return psDefaultRoute{}, errors.New("vpn: не найден физический маршрут по умолчанию — неизвестно, куда вернуть трафик к внешнему endpoint")
	}
	return route, nil
}

// applyDNS перенаправляет системный DNS на адрес из подсети TUN. DNS физических
// интерфейсов запоминается и возвращается при откате: иначе Windows продолжит
// спрашивать провайдерский резолвер в обход туннеля (утечка DNS).
func (w *windowsRoutes) applyDNS(ctx context.Context, rec *record, plan Plan) error {
	if !plan.DNS.IsValid() {
		return nil
	}
	interfaces, err := w.dnsInterfaces(ctx, rec, plan.TUNName)
	if err != nil {
		msg := "не удалось прочитать DNS физических интерфейсов: системный DNS не перенастроен — возможна утечка DNS"
		rec.dnsWarn = msg
		rec.warn(msg)
		rec.log("%s (%v)", msg, err)
	} else {
		for _, iface := range interfaces {
			argv := psCmd(fmt.Sprintf(
				"Set-DnsClientServerAddress -InterfaceIndex %d -ServerAddresses %s -ErrorAction Stop",
				iface.InterfaceIndex, psQuote(plan.DNS.String())))
			inverse := psCmd(restoreDNS(iface))
			if err := rec.do(ctx, argv, inverse); err != nil {
				return err
			}
		}
	}

	// DNS самого TUN нужен, чтобы запросы уходили в мост, а не в пустоту.
	argv := psCmd(fmt.Sprintf(
		"Set-DnsClientServerAddress -InterfaceAlias %s -ServerAddresses %s -ErrorAction Stop",
		psQuote(plan.TUNName), psQuote(plan.DNS.String())))
	inverse := psCmd(fmt.Sprintf(
		"Set-DnsClientServerAddress -InterfaceAlias %s -ResetServerAddresses -ErrorAction SilentlyContinue",
		psQuote(plan.TUNName)))
	if err := rec.do(ctx, argv, inverse); err != nil {
		return err
	}
	if err := rec.do(ctx, psCmd(clearDNSCacheScript), nil); err != nil {
		// Кэш — не повод валить туннель: маршруты и DNS уже применены.
		rec.warn("не удалось сбросить кэш DNS: " + err.Error())
	}
	return nil
}

// dnsInterfaces читает DNS физических интерфейсов, которые придётся увести в TUN.
func (w *windowsRoutes) dnsInterfaces(ctx context.Context, rec *record, tun string) ([]psDNS, error) {
	script := fmt.Sprintf(`$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false);
$items = Get-DnsClientServerAddress -AddressFamily IPv4 -ErrorAction Stop |
  Where-Object { $_.ServerAddresses.Count -gt 0 -and $_.InterfaceAlias -ne %s } |
  Select-Object InterfaceAlias, InterfaceIndex, ServerAddresses
if ($null -eq $items) { '[]' } else { @($items) | ConvertTo-Json -Compress }`, psQuote(tun))
	out, err := rec.read(ctx, psExe, psArgs(script)...)
	if err != nil {
		return nil, err
	}
	return parsePSDNS(out)
}

// parsePSDNS разбирает ответ ConvertTo-Json: объект для одной записи, массив
// для нескольких, пусто — ни одной.
func parsePSDNS(out string) ([]psDNS, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, nil
	}
	if strings.HasPrefix(text, "[") {
		var list []psDNS
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var one psDNS
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		return nil, err
	}
	return []psDNS{one}, nil
}

// restoreDNS — команда отмены: вернуть прежние серверы интерфейса или снять
// настройку, если их не было.
func restoreDNS(iface psDNS) string {
	if len(iface.ServerAddresses) == 0 {
		return fmt.Sprintf("Set-DnsClientServerAddress -InterfaceIndex %d -ResetServerAddresses -ErrorAction SilentlyContinue", iface.InterfaceIndex)
	}
	quoted := make([]string, len(iface.ServerAddresses))
	for i, s := range iface.ServerAddresses {
		quoted[i] = psQuote(s)
	}
	return fmt.Sprintf("Set-DnsClientServerAddress -InterfaceIndex %d -ServerAddresses %s -ErrorAction Stop",
		iface.InterfaceIndex, strings.Join(quoted, ","))
}

// RevertTunState — аварийный откат по файлу состояния (команда `tun-revert`).
// Читает журнал применённых команд и выполняет их отмену в обратном порядке:
// полностью откаченное состояние удаляется, невыполненные отмены остаются в
// файле, чтобы повторный запуск продолжил с места сбоя. Возвращает, сколько
// шагов было в файле.
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
	}
	return len(steps), rec.revert(ctx)
}

// ReadCurrentDNS определяет DNS текущего подключения для пресета «DNS
// провайдера». Провайдерские DNS часто приватные (192.168.x.1) и в TUN могут
// быть недоступны — это не ошибка, а предупреждение в интерфейсе.
func ReadCurrentDNS(ctx context.Context, exec Executor) (DNSInfo, error) {
	if exec == nil {
		exec = osExecutor{}
	}
	out, err := exec.Output(ctx, psExe, psArgs(currentDNSScript)...)
	if err != nil {
		return DNSInfo{}, fmt.Errorf("vpn: определение DNS провайдера: %w", err)
	}
	var raw struct {
		Interface string
		Servers   []string
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return DNSInfo{}, fmt.Errorf("vpn: разбор DNS провайдера: %w", err)
	}
	info := DNSInfo{Interface: raw.Interface, Servers: raw.Servers}
	if len(info.Servers) == 0 {
		return info, errors.New("vpn: не удалось определить DNS провайдера")
	}
	allLoopback := true
	for _, server := range info.Servers {
		host := server
		if h, _, err := net.SplitHostPort(server); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			allLoopback = false
			break
		}
	}
	if allLoopback {
		info.Warning = "DNS провайдера указывает на loopback-заглушку; в TUN он может не работать"
	}
	return info, nil
}

// psExe — PowerShell: командлеты NetTCPIP/DnsClient есть на Windows 10 и 11.
const psExe = "powershell.exe"

// tunIPv6Addr — адрес TUN для маршрута ::/0. IPv6-трафик в мосте всё равно
// отбрасывается: адрес нужен только для того, чтобы Windows не отправила его
// мимо туннеля.
const tunIPv6Addr = "fd00:198:18::1"

// psArgs собирает аргументы запуска скрипта.
func psArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}

// psCmd — команда целиком, в виде argv для Executor и журнала отката.
func psCmd(script string) []string {
	return append([]string{psExe}, psArgs(script)...)
}

// psQuote — строковый литерал PowerShell в одинарных кавычках.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

const defaultRouteScript = `$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false);
$r = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop |
  Sort-Object -Property RouteMetric, InterfaceMetric | Select-Object -First 1
if ($null -eq $r) { throw 'маршрут по умолчанию не найден' }
$r | Select-Object InterfaceAlias, InterfaceIndex, NextHop | ConvertTo-Json -Compress`

const currentDNSScript = `$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false);
$r = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop |
  Sort-Object -Property RouteMetric, InterfaceMetric | Select-Object -First 1
if ($null -eq $r) { throw 'маршрут по умолчанию не найден' }
$servers = @()
$cfg = Get-DnsClientServerAddress -InterfaceIndex $r.InterfaceIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue
foreach ($s in $cfg.ServerAddresses) { if ($s) { $servers += $s } }
[pscustomobject]@{ Interface = $r.InterfaceAlias; Servers = $servers } | ConvertTo-Json -Compress`

const clearDNSCacheScript = `Clear-DnsClientCache -ErrorAction SilentlyContinue`

// osExecutor выполняет команды через os/exec. Группу процессов на Windows не
// заводим: SIGINT дочернему процессу всё равно не доставить, остановкой
// занимается сам warpgen (контекст и StopTimeout).
type osExecutor struct{}

func (osExecutor) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", commandError(name, err, stderr.String())
	}
	return string(out), nil
}

func (osExecutor) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
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
