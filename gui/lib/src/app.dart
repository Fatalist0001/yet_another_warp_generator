/// Корень GUI: поиск бинарника, тема и оболочка с двумя вкладками.
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';

import 'app_theme.dart';
import 'controllers/account_controller.dart';
import 'controllers/services_controller.dart';
import 'controllers/settings_controller.dart';
import 'controllers/tunnel_controller.dart';
import 'pages/settings_page.dart';
import 'pages/setup_page.dart';
import 'pages/tunnel_page.dart';
import 'platform/app_paths.dart';
import 'platform/process_runner.dart';
import 'platform/tray_channel.dart';
import 'platform/warpgen_locator.dart';
import 'services/settings_store.dart';
import 'services/warpgen_cli.dart';

/// Собранные зависимости приложения.
class AppDependencies {
  const AppDependencies({
    required this.cli,
    required this.homeDir,
    required this.dataDir,
  });

  final WarpgenCli cli;
  final String homeDir;

  /// Каталог данных: `~/.warpgen` или портативный `warpgen-data` рядом с exe.
  final String dataDir;

  /// Каталог identity warp-plus и кэша Psiphon.
  String get cacheDir => '$dataDir/chain';

  /// Файл настроек GUI.
  String get settingsPath => '$dataDir/gui.json';

  /// Кэш endpoint'ов, выбранных по скорости при первой настройке.
  String get endpointCachePath => '$dataDir/endpoints.json';
}

/// Включать ли системный туннель при старте.
///
/// Android — всегда: TUN создаёт VpnService, иначе VPN не будет вообще.
/// Windows — по умолчанию на первом запуске (портативная сборка и так идёт с
/// правами администратора, а рядовому пользователю нужен весь трафик, а не
/// SOCKS5); дальше решает сохранённый выбор. Linux — только сохранённый выбор.
bool initialTunEnabled({
  required bool isAndroid,
  required bool isWindows,
  required bool firstRun,
  required bool saved,
}) {
  if (isAndroid) return true;
  if (isWindows) return firstRun || saved;
  return saved;
}

/// Приложение warpgen.
class WarpgenApp extends StatefulWidget {
  const WarpgenApp({
    super.key,
    this.locator,
    this.runner,
    this.environment,
    this.dataDir,
  });

  /// Поиск бинарника (в тестах подменяется).
  final WarpgenLocator? locator;

  /// Запуск процессов (в тестах подменяется).
  final ProcessRunner? runner;

  /// Окружение: `HOME`, `PATH`, `WARPGEN_BIN`.
  final Map<String, String>? environment;

  /// Каталог данных рядом с exe (Windows). `null` — `~/.warpgen`.
  final String? dataDir;

  @override
  State<WarpgenApp> createState() => _WarpgenAppState();
}

class _WarpgenAppState extends State<WarpgenApp> {
  ThemeMode _themeMode = ThemeMode.system;

  void _setThemeMode(ThemeMode mode) {
    if (_themeMode == mode) return;
    setState(() => _themeMode = mode);
  }

  @override
  Widget build(BuildContext context) {
    final environment = widget.environment ?? Platform.environment;
    return MaterialApp(
      title: 'Товарищ майор',
      debugShowCheckedModeBanner: false,
      theme: AppTheme.light(),
      darkTheme: AppTheme.dark(),
      themeMode: _themeMode,
      home: _Root(
        locator: widget.locator ?? WarpgenLocator(environment: environment),
        runner: widget.runner ?? const IoProcessRunner(),
        environment: environment,
        dataDir: widget.dataDir,
        onThemeModeChanged: _setThemeMode,
      ),
    );
  }
}

/// Загружает зависимости и решает, что показать: оболочку или экран настройки.
class _Root extends StatefulWidget {
  const _Root({
    required this.locator,
    required this.runner,
    required this.environment,
    required this.dataDir,
    required this.onThemeModeChanged,
  });

  final WarpgenLocator locator;
  final ProcessRunner runner;
  final Map<String, String> environment;
  final String? dataDir;
  final ValueChanged<ThemeMode> onThemeModeChanged;

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> with WidgetsBindingObserver {
  bool _loading = true;
  String? _notFoundPath;
  AppDependencies? _deps;
  TunnelController? _tunnel;
  AccountController? _account;
  SettingsController? _settings;
  ServicesController? _services;
  bool _wasLive = false;
  late final TrayChannel _trayChannel;

  /// Выход из трея уже начат: повторный клик не должен останавливать дважды.
  bool _quitting = false;

  String get _homeDir => homeDirectory(widget.environment);

  /// Портативный каталог данных; без него — прежний `~/.warpgen`.
  String get _dataDir => widget.dataDir ?? '$_homeDir/.warpgen';

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _trayChannel = TrayChannel();
    _trayChannel.setupTrayHandlers(
      // «Остановить туннель» в трее: окно остаётся открытым, поэтому нужен
      // обычный stop() со статусом, а не shutdown() для умирающего окна.
      onStop: () async {
        await _tunnel?.stop();
      },
      onQuit: _quitFromTray,
    );
    unawaited(_bootstrap());
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.detached) {
      _tunnel?.shutdown();
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _trayChannel.dispose();
    _disposeControllers();
    super.dispose();
  }

  /// «Выход» в меню трея: сначала снимаем туннель, только потом закрываем
  /// процесс. Иначе остались бы применённые маршруты и дочерний CLI, и
  /// следующее подключение ломалось бы на конфликте маршрутов.
  Future<void> _quitFromTray() async {
    if (_quitting) return;
    _quitting = true;
    try {
      await _tunnel?.stop();
    } finally {
      await _trayChannel.quit();
    }
  }

  void _disposeControllers() {
    _settings?.removeListener(_onSettingsChanged);
    _tunnel?.removeListener(_onTunnelChanged);
    _tunnel?.dispose();
    _services?.dispose();
    _account?.dispose();
    _settings?.dispose();
    _tunnel = null;
    _services = null;
    _account = null;
    _settings = null;
    _wasLive = false;
  }

  /// Ищет бинарник и готовит контроллеры. `false` — не нашли.
  Future<bool> _bootstrap({String? explicitPath}) async {
    setState(() => _loading = true);
    final located = await widget.locator.locate(explicitPath: explicitPath);
    if (!mounted) return false;

    if (located == null) {
      setState(() {
        _loading = false;
        _notFoundPath = explicitPath;
      });
      return false;
    }

    // В портативном режиме аккаунт тоже лежит рядом с exe, а не рядом с CLI:
    // папку с программой тогда можно перенести целиком.
    final location = widget.dataDir == null
        ? located
        : WarpgenLocation(
            binaryPath: located.binaryPath,
            accountFile: '$_dataDir/warpgen-account.json',
            source: located.source,
          );

    final deps = AppDependencies(
      cli: WarpgenCli(
        runner: widget.runner,
        location: location,
        cacheDir: '$_dataDir/chain',
        // AppImage смонтирован только для пользователя: root не прочитает
        // бинарник, а pkexec проверяет доступ до запроса пароля.
        elevDir: '$_dataDir/elev',
        // Остановка из GUI: процесс под pkexec — root, а на Windows сигналы
        // дочернему процессу не доставляются. CLI проверяет файл сам.
        stopFile: '$_dataDir/run/stop',
        // Журнал отката не должен жить рядом с exe: портативную папку
        // переносят и удаляют, а применённые маршруты и DNS остаются в системе.
        // На Windows CLI сам берёт каталог из профиля (%LOCALAPPDATA%); на
        // Linux профиль и так ~/.warpgen, поэтому путь передаём только там.
        stateDir: Platform.isWindows ? null : '$_dataDir/state',
        endpointCache: '$_dataDir/endpoints.json',
      ),
      homeDir: _homeDir,
      dataDir: _dataDir,
    );

    final account = AccountController(cli: deps.cli);
    // Первый запуск определяем до load(): он может сохранить gui.json
    // (например, если в старом файле был удалённый DNS-пресет).
    final firstRun = !File(deps.settingsPath).existsSync();
    final settings = SettingsController(
      cli: deps.cli,
      store: SettingsStore(path: deps.settingsPath),
    );
    await settings.load();
    if (!mounted) return false;

    final tunnel = TunnelController(
      cli: deps.cli,
      warpOnly: settings.settings.warpOnly,
      // На Android системный туннель — единственный режим, который даёт VPN
      // для других приложений: TUN создаёт VpnService, а не CLI под root.
      // На Windows это режим по умолчанию при первом запуске: портативная
      // сборка и так запускается с правами администратора, а отдавать
      // рядовому пользователю один SOCKS5 незачем. Выключить можно
      // переключателем — тогда выбор сохраняется в gui.json.
      tun: initialTunEnabled(
        isAndroid: Platform.isAndroid,
        isWindows: Platform.isWindows,
        firstRun: firstRun,
        saved: settings.settings.tun,
      ),
      countries: settings.settings.countries,
      bind: settings.settings.bind,
      dnsResolver: settings.resolveDns,
      ensureAccount: account.ensure,
    );
    final services = ServicesController(cli: deps.cli);

    _disposeControllers();
    _tunnel = tunnel;
    _account = account;
    _settings = settings;
    _services = services;
    settings.addListener(_onSettingsChanged);
    tunnel.addListener(_onTunnelChanged);
    widget.onThemeModeChanged(settings.settings.theme);

    setState(() {
      _deps = deps;
      _loading = false;
      _notFoundPath = null;
    });
    unawaited(account.refresh());
    return true;
  }

  void _onSettingsChanged() {
    final settings = _settings;
    if (settings == null) return;
    widget.onThemeModeChanged(settings.settings.theme);
  }

  void _onTunnelChanged() {
    final tunnel = _tunnel;
    final settings = _settings;
    final services = _services;
    if (tunnel == null || settings == null || services == null) return;

    if (tunnel.isLive && !_wasLive) {
      _wasLive = true;
      if (settings.settings.checkServices) {
        final socks = tunnel.socks;
        if (socks != null && socks.trim().isNotEmpty) {
          unawaited(services.check(socks));
        }
      }
    } else if (!tunnel.isLive && _wasLive) {
      _wasLive = false;
      services.reset();
    }

    settings.updateConnection(
      warpOnly: tunnel.warpOnly,
      tun: tunnel.tun,
      countries: tunnel.countries,
      bind: tunnel.bind,
    );
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    final deps = _deps;
    if (deps == null) {
      return SetupPage(
        onLocate: (path) => _bootstrap(explicitPath: path),
        initialPath: _notFoundPath ?? '',
      );
    }
    return _Shell(
      deps: deps,
      tunnel: _tunnel!,
      account: _account!,
      settings: _settings!,
      services: _services!,
      onRelocate: (path) => _bootstrap(explicitPath: path),
    );
  }
}

/// Оболочка: две нижние вкладки — подключение и настройки.
///
/// Вкладки не используют `TabBarView`: он уничтожал невидимую страницу и
/// раскладывал обе при каждом переключении. `IndexedStack` держит страницы
/// живыми, а вторая создаётся только при первом открытии.
class _Shell extends StatefulWidget {
  const _Shell({
    required this.deps,
    required this.tunnel,
    required this.account,
    required this.settings,
    required this.services,
    required this.onRelocate,
  });

  final AppDependencies deps;
  final TunnelController tunnel;
  final AccountController account;
  final SettingsController settings;
  final ServicesController services;
  final Future<bool> Function(String? path) onRelocate;

  @override
  State<_Shell> createState() => _ShellState();
}

class _ShellState extends State<_Shell> {
  int _index = 0;

  // Страницы создаются один раз. Если строить их в `build`, то каждое
  // переключение вкладки пересобирало обе страницы, включая скрытую.
  late Widget _tunnelPage;
  Widget? _settingsPage;

  @override
  void initState() {
    super.initState();
    _tunnelPage = _buildTunnelPage();
  }

  @override
  void didUpdateWidget(covariant _Shell oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (!identical(oldWidget.tunnel, widget.tunnel) ||
        !identical(oldWidget.services, widget.services) ||
        !identical(oldWidget.deps, widget.deps) ||
        !identical(oldWidget.account, widget.account) ||
        !identical(oldWidget.settings, widget.settings)) {
      _tunnelPage = _buildTunnelPage();
      _settingsPage = _index == 1 ? _buildSettingsPage() : null;
    }
  }

  Widget _buildTunnelPage() {
    return TunnelPage(tunnel: widget.tunnel, services: widget.services);
  }

  Widget _buildSettingsPage() {
    return SettingsPage(
      deps: widget.deps,
      tunnel: widget.tunnel,
      account: widget.account,
      settings: widget.settings,
      services: widget.services,
      onRelocate: widget.onRelocate,
    );
  }

  void _selectTab(int index) {
    if (index == _index) return;
    setState(() {
      _index = index;
      if (index == 1) _settingsPage ??= _buildSettingsPage();
    });
  }

  Widget _tabButton(BuildContext context, int index, String label) {
    final scheme = Theme.of(context).colorScheme;
    final selected = _index == index;
    return Expanded(
      child: Semantics(
        button: true,
        selected: selected,
        child: TextButton(
          key: ValueKey('tab-$index'),
          onPressed: () => _selectTab(index),
          style: TextButton.styleFrom(
            foregroundColor: selected ? scheme.primary : scheme.onSurfaceVariant,
            backgroundColor:
                selected ? scheme.primaryContainer : Colors.transparent,
            // Без splash и анимации: на программном рендеринге они заметно
            // нагружали CPU, хотя переключение всего лишь меняет страницу.
            splashFactory: NoSplash.splashFactory,
            overlayColor: Colors.transparent,
            shape: const RoundedRectangleBorder(
              borderRadius: BorderRadius.all(Radius.circular(10)),
            ),
            padding: const EdgeInsets.symmetric(vertical: 12),
          ),
          child: Text(label),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Scaffold(
      appBar: AppBar(title: const Text('Товарищ майор')),
      bottomNavigationBar: DecoratedBox(
        decoration: BoxDecoration(
          color: scheme.surface,
          border: Border(top: BorderSide(color: scheme.outlineVariant)),
        ),
        child: SafeArea(
          top: false,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 4, 12, 4),
            child: Row(
              children: [
                _tabButton(context, 0, 'Подключение'),
                const SizedBox(width: 8),
                _tabButton(context, 1, 'Настройки'),
              ],
            ),
          ),
        ),
      ),
      body: IndexedStack(
        index: _index,
        children: [
          TickerMode(enabled: _index == 0, child: _tunnelPage),
          TickerMode(
            enabled: _index == 1,
            child: _settingsPage ?? const SizedBox.shrink(),
          ),
        ],
      ),
    );
  }
}
