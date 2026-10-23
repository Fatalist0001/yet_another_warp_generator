import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app.dart';
import 'package:warpgen_gui/src/app_theme.dart';
import 'package:warpgen_gui/src/controllers/account_controller.dart';
import 'package:warpgen_gui/src/controllers/tunnel_controller.dart';
import 'package:warpgen_gui/src/pages/tunnel_page.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';

import 'support/fakes.dart';

const _binaryPath = '/opt/warpgen/warpgen';
const _homeDir = '/tmp/warpgen-gui-home';

/// Окружение, в котором GUI находит бинарник через WARPGEN_BIN.
Map<String, String> _env({bool withBinary = true}) => {
      'PATH': '',
      'HOME': _homeDir,
      if (withBinary) 'WARPGEN_BIN': _binaryPath,
    };

/// Высокое окно: карточки главного экрана не ленивые, но в 600 px не влезают.
void _useTallWindow(WidgetTester tester) {
  tester.view.physicalSize = const Size(700, 1600);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

WarpgenLocator _locator({required bool withBinary}) {
  return WarpgenLocator(
    environment: _env(withBinary: withBinary),
    workingDirectory: Directory('/tmp'),
    fileExists: (path) => withBinary && path == _binaryPath,
  );
}

void main() {
  testWidgets('без бинарника показывает экран настройки', (tester) async {
    await tester.pumpWidget(
      WarpgenApp(
        locator: _locator(withBinary: false),
        runner: FakeProcessRunner(),
        environment: _env(withBinary: false),
      ),
    );
    await tester.pump();

    expect(find.text('Не нашли warpgen'), findsOneWidget);
    expect(find.text('Найти и запустить'), findsOneWidget);
  });

  testWidgets('с бинарником показывает оболочку и главный экран',
      (tester) async {
    _useTallWindow(tester);
    await tester.pumpWidget(
      WarpgenApp(
        locator: _locator(withBinary: true),
        runner: FakeProcessRunner(),
        environment: _env(),
      ),
    );
    await tester.pump();
    await tester.pump();

    expect(find.text('Туннель'), findsWidgets);
    expect(find.text('Подключиться'), findsOneWidget);
    expect(find.text('Режим'), findsOneWidget);
    expect(find.text('Журнал'), findsOneWidget);
    expect(find.byType(NavigationBar), findsOneWidget);
  });

  testWidgets('на широком окне навигация уезжает в рейл', (tester) async {
    tester.view.physicalSize = const Size(1400, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    await tester.pumpWidget(
      WarpgenApp(
        locator: _locator(withBinary: true),
        runner: FakeProcessRunner(),
        environment: _env(),
      ),
    );
    await tester.pump();
    await tester.pump();

    expect(find.byType(NavigationRail), findsOneWidget);
    expect(find.byType(NavigationBar), findsNothing);
  });

  testWidgets('после ready карточка показывает прокси и страну',
      (tester) async {
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final account = AccountController(cli: cli);

    final process = runner.enqueue();
    await tunnel.start();
    process.emitEvent(const {
      'event': 'ready',
      'socks': '127.0.0.1:8086',
      'country': 'NL',
      'ip': '203.0.113.9',
      'seconds': 12,
    });

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.light(),
        home: Scaffold(
          body: TunnelPage(tunnel: tunnel, account: account),
        ),
      ),
    );
    await tester.pump();

    expect(find.text('socks5h://127.0.0.1:8086'), findsOneWidget);
    expect(find.text('Подключено'), findsOneWidget);
    expect(find.text('203.0.113.9'), findsOneWidget);
    expect(find.text('NL'), findsWidgets);
    expect(find.text('Отключиться'), findsOneWidget);
    expect(find.text('Подключиться'), findsNothing);

    await process.finish(0);
    await tester.pump();
  });

  testWidgets('пустой журнал подсказывает, что делать', (tester) async {
    _useTallWindow(tester);
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final account = AccountController(cli: cli);
    // Чтение файла аккаунта — настоящий ввод-вывод, выносим его из fake async.
    await tester.runAsync(account.refresh);

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.dark(),
        home: Scaffold(
          body: TunnelPage(tunnel: tunnel, account: account),
        ),
      ),
    );
    await tester.pump();

    expect(find.textContaining('Пока пусто'), findsOneWidget);
    // Аккаунта нет — кнопка подключения недоступна и есть подсказка.
    expect(find.textContaining('зарегистрируйте аккаунт'), findsOneWidget);
  });

  testWidgets('переключатель системного туннеля меняет состояние контроллера',
      (tester) async {
    _useTallWindow(tester);
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final account = AccountController(cli: cli);

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.light(),
        home: Scaffold(
          body: TunnelPage(tunnel: tunnel, account: account),
        ),
      ),
    );
    await tester.pump();

    expect(find.text('Системный туннель (root)'), findsOneWidget);
    expect(tunnel.tun, isFalse);

    await tester.tap(find.text('Системный туннель (root)'));
    await tester.pump();

    expect(tunnel.tun, isTrue);
  });
}
