/// Корень GUI: поиск бинарника, тема и оболочка с разделами.
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';

import 'app_theme.dart';
import 'controllers/account_controller.dart';
import 'controllers/scan_controller.dart';
import 'controllers/tunnel_controller.dart';
import 'pages/account_page.dart';
import 'pages/scan_page.dart';
import 'pages/setup_page.dart';
import 'pages/tunnel_page.dart';
import 'platform/process_runner.dart';
import 'platform/warpgen_locator.dart';
import 'services/warpgen_cli.dart';

/// Собранные зависимости приложения.
class AppDependencies {
  const AppDependencies({required this.cli, required this.homeDir});

  final WarpgenCli cli;
  final String homeDir;

  /// Каталог identity warp-plus и кэша Psiphon.
  String get cacheDir => '$homeDir/.warpgen/chain';

  /// Куда сохраняются сгенерированные конфиги.
  String get configDir => '$homeDir/.warpgen/conf';
}

/// Приложение warpgen.
class WarpgenApp extends StatefulWidget {
  const WarpgenApp({super.key, this.locator, this.runner, this.environment});

  /// Поиск бинарника (в тестах подменяется).
  final WarpgenLocator? locator;

  /// Запуск процессов (в тестах подменяется).
  final ProcessRunner? runner;

  /// Окружение: `HOME`, `PATH`, `WARPGEN_BIN`.
  final Map<String, String>? environment;

  @override
  State<WarpgenApp> createState() => _WarpgenAppState();
}

class _WarpgenAppState extends State<WarpgenApp> {
  ThemeMode _themeMode = ThemeMode.system;

  void _toggleTheme() {
    setState(() {
      _themeMode =
          _themeMode == ThemeMode.dark ? ThemeMode.light : ThemeMode.dark;
    });
  }

  @override
  Widget build(BuildContext context) {
    final environment = widget.environment ?? Platform.environment;
    return MaterialApp(
      title: 'warpgen',
      debugShowCheckedModeBanner: false,
      theme: AppTheme.light(),
      darkTheme: AppTheme.dark(),
      themeMode: _themeMode,
      home: _Root(
        locator: widget.locator ?? WarpgenLocator(environment: environment),
        runner: widget.runner ?? const IoProcessRunner(),
        environment: environment,
        onToggleTheme: _toggleTheme,
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
    required this.onToggleTheme,
  });

  final WarpgenLocator locator;
  final ProcessRunner runner;
  final Map<String, String> environment;
  final VoidCallback onToggleTheme;

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> with WidgetsBindingObserver {
  bool _loading = true;
  String? _notFoundPath;
  AppDependencies? _deps;
  TunnelController? _tunnel;
  ScanController? _scan;
  AccountController? _account;

  String get _homeDir {
    final home = widget.environment['HOME'];
    if (home != null && home.trim().isNotEmpty) return home.trim();
    return Directory.current.path;
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
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
    _tunnel?.dispose();
    _scan?.dispose();
    _account?.dispose();
    super.dispose();
  }

  /// Ищет бинарник и готовит контроллеры. `false` — не нашли.
  Future<bool> _bootstrap({String? explicitPath}) async {
    setState(() => _loading = true);
    final location = await widget.locator.locate(explicitPath: explicitPath);
    if (!mounted) return false;

    if (location == null) {
      setState(() {
        _loading = false;
        _notFoundPath = explicitPath;
      });
      return false;
    }

    final deps = AppDependencies(
      cli: WarpgenCli(
        runner: widget.runner,
        location: location,
        cacheDir: '$_homeDir/.warpgen/chain',
      ),
      homeDir: _homeDir,
    );

    _tunnel?.dispose();
    _scan?.dispose();
    _account?.dispose();

    final tunnel = TunnelController(cli: deps.cli);
    final scan = ScanController(cli: deps.cli, outputDir: deps.configDir);
    final account = AccountController(cli: deps.cli);

    setState(() {
      _deps = deps;
      _tunnel = tunnel;
      _scan = scan;
      _account = account;
      _loading = false;
      _notFoundPath = null;
    });
    unawaited(account.refresh());
    return true;
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    final deps = _deps;
    if (deps == null) {
      return SetupPage(
        onToggleTheme: widget.onToggleTheme,
        onLocate: (path) => _bootstrap(explicitPath: path),
        initialPath: _notFoundPath ?? '',
      );
    }
    return _Shell(
      deps: deps,
      tunnel: _tunnel!,
      scan: _scan!,
      account: _account!,
      onToggleTheme: widget.onToggleTheme,
      onRelocate: (path) => _bootstrap(explicitPath: path),
    );
  }
}

/// Оболочка: навигация снизу на узком окне и рейл — на широком.
class _Shell extends StatefulWidget {
  const _Shell({
    required this.deps,
    required this.tunnel,
    required this.scan,
    required this.account,
    required this.onToggleTheme,
    required this.onRelocate,
  });

  final AppDependencies deps;
  final TunnelController tunnel;
  final ScanController scan;
  final AccountController account;
  final VoidCallback onToggleTheme;
  final Future<bool> Function(String? path) onRelocate;

  @override
  State<_Shell> createState() => _ShellState();
}

class _ShellState extends State<_Shell> {
  int _index = 0;

  List<Widget> _actions(BuildContext context) {
    return [
      IconButton(
        tooltip: 'Сменить тему',
        onPressed: widget.onToggleTheme,
        icon: const Icon(Icons.brightness_6_outlined),
      ),
      IconButton(
        tooltip: 'Путь к warpgen',
        onPressed: () => _showBinaryDialog(context),
        icon: const Icon(Icons.settings_outlined),
      ),
    ];
  }

  Future<void> _showBinaryDialog(BuildContext context) async {
    final controller = TextEditingController(text: widget.deps.cli.binaryPath);
    final path = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Бинарник warpgen'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'GUI запускает тот же CLI, что и терминал. Укажите путь к бинарнику '
              'или оставьте поиск по PATH и переменной WARPGEN_BIN.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 16),
            TextField(
              controller: controller,
              autofocus: true,
              decoration: const InputDecoration(
                labelText: 'Путь к warpgen',
                hintText: '/home/user/warpgen/warpgen',
              ),
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(controller.text.trim()),
            child: const Text('Проверить'),
          ),
        ],
      ),
    );
    controller.dispose();
    if (path == null || path.isEmpty) return;
    final found = await widget.onRelocate(path);
    if (!found && context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Не нашли бинарник по пути $path')),
      );
    }
  }

  @override
  Widget build(BuildContext context) {
    final actions = _actions(context);
    final pages = <Widget>[
      TunnelPage(
        tunnel: widget.tunnel,
        account: widget.account,
        actions: actions,
      ),
      ScanPage(scan: widget.scan, account: widget.account, actions: actions),
      AccountPage(account: widget.account, actions: actions),
    ];

    return LayoutBuilder(
      builder: (context, constraints) {
        final wide = constraints.maxWidth >= 840;
        if (wide) {
          return Scaffold(
            body: Row(
              children: [
                NavigationRail(
                  selectedIndex: _index,
                  onDestinationSelected: (index) =>
                      setState(() => _index = index),
                  labelType: NavigationRailLabelType.all,
                  leading: Padding(
                    padding: const EdgeInsets.symmetric(vertical: 16),
                    child: Column(
                      children: [
                        Icon(
                          Icons.shield_moon_outlined,
                          color: Theme.of(context).colorScheme.primary,
                        ),
                        const SizedBox(height: 4),
                        Text(
                          'warpgen',
                          style: Theme.of(context).textTheme.labelSmall,
                        ),
                      ],
                    ),
                  ),
                  destinations: const [
                    NavigationRailDestination(
                      icon: Icon(Icons.lan_outlined),
                      selectedIcon: Icon(Icons.lan),
                      label: Text('Туннель'),
                    ),
                    NavigationRailDestination(
                      icon: Icon(Icons.radar_outlined),
                      selectedIcon: Icon(Icons.radar),
                      label: Text('Скан'),
                    ),
                    NavigationRailDestination(
                      icon: Icon(Icons.person_outline),
                      selectedIcon: Icon(Icons.person),
                      label: Text('Аккаунт'),
                    ),
                  ],
                ),
                const VerticalDivider(width: 1),
                Expanded(child: pages[_index]),
              ],
            ),
          );
        }

        return Scaffold(
          body: pages[_index],
          bottomNavigationBar: NavigationBar(
            selectedIndex: _index,
            onDestinationSelected: (index) => setState(() => _index = index),
            destinations: const [
              NavigationDestination(
                icon: Icon(Icons.lan_outlined),
                selectedIcon: Icon(Icons.lan),
                label: 'Туннель',
              ),
              NavigationDestination(
                icon: Icon(Icons.radar_outlined),
                selectedIcon: Icon(Icons.radar),
                label: 'Скан',
              ),
              NavigationDestination(
                icon: Icon(Icons.person_outline),
                selectedIcon: Icon(Icons.person),
                label: 'Аккаунт',
              ),
            ],
          ),
        );
      },
    );
  }
}
