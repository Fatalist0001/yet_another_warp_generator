import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app.dart';
import 'package:warpgen_gui/src/app_theme.dart';
import 'package:warpgen_gui/src/controllers/services_controller.dart';
import 'package:warpgen_gui/src/controllers/tunnel_controller.dart';
import 'package:warpgen_gui/src/pages/tunnel_page.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';

import 'support/fakes.dart';

const _binaryPath = '/opt/warpgen/warpgen';

Map<String, String> _env(Directory home, {bool withBinary = true}) => {
      'PATH': '',
      'HOME': home.path,
      if (withBinary) 'WARPGEN_BIN': _binaryPath,
    };

Directory _home() {
  final dir = Directory.systemTemp.createTempSync('warpgen-gui-home');
  addTearDown(() {
    if (dir.existsSync()) dir.deleteSync(recursive: true);
  });
  return dir;
}

/// Высокое окно: терминал и настройки не должны обрезаться.
void _useTallWindow(WidgetTester tester) {
  tester.view.physicalSize = const Size(700, 1800);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

WarpgenLocator _locator({
  required Directory home,
  required bool withBinary,
}) {
  return WarpgenLocator(
    environment: _env(home, withBinary: withBinary),
    workingDirectory: Directory('/tmp'),
    fileExists: (path) => withBinary && path == _binaryPath,
  );
}

/// Даёт настоящему вводу-выводу (настройки и аккаунт) завершиться.
Future<void> _pumpApp(WidgetTester tester, Widget app) async {
  await tester.pumpWidget(app);
  await tester.runAsync(() async {
    await Future<void>.delayed(const Duration(milliseconds: 100));
  });
  await tester.pump();
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('без бинарника показывает экран настройки', (tester) async {
    final home = _home();
    await tester.pumpWidget(
      WarpgenApp(
        locator: _locator(home: home, withBinary: false),
        runner: FakeProcessRunner(),
        environment: _env(home, withBinary: false),
      ),
    );
    await tester.pump();

    expect(find.text('Не нашли warpgen'), findsOneWidget);
    expect(find.text('Найти и запустить'), findsOneWidget);
  });

  testWidgets('с бинарником показывает две горизонтальные вкладки',
      (tester) async {
    _useTallWindow(tester);
    final home = _home();
    await _pumpApp(
      tester,
      WarpgenApp(
        locator: _locator(home: home, withBinary: true),
        runner: FakeProcessRunner(),
        environment: _env(home),
      ),
    );

    expect(find.byType(TabBar), findsOneWidget);
    expect(find.text('Подключение'), findsOneWidget);
    expect(find.text('Настройки'), findsOneWidget);
    expect(find.text('Подключиться'), findsOneWidget);
    expect(find.text('WARP'), findsOneWidget);
    expect(find.text('WARP + Psiphon'), findsOneWidget);
    expect(find.text('Системный туннель'), findsOneWidget);
    expect(find.text('Терминал'), findsOneWidget);
    expect(find.byType(NavigationBar), findsNothing);
    expect(find.byType(NavigationRail), findsNothing);
  });

  testWidgets('вкладка настроек показывает DNS и аккаунт', (tester) async {
    _useTallWindow(tester);
    final home = _home();
    await _pumpApp(
      tester,
      WarpgenApp(
        locator: _locator(home: home, withBinary: true),
        runner: FakeProcessRunner(),
        environment: _env(home),
      ),
    );

    await tester.tap(find.text('Настройки'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 500));

    expect(find.text('DNS'), findsOneWidget);
    expect(find.text('Аккаунт WARP'), findsOneWidget);
    expect(find.text('Пересоздать конфигурацию'), findsOneWidget);
    expect(find.text('Приложение'), findsOneWidget);
  });

  testWidgets('после ready экран показывает прокси и страну', (tester) async {
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final services = ServicesController(cli: cli);

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
        home: Scaffold(body: TunnelPage(tunnel: tunnel, services: services)),
      ),
    );
    await tester.pump();

    expect(find.text('socks5h://127.0.0.1:8086'), findsOneWidget);
    expect(find.text('Подключено'), findsOneWidget);
    expect(find.textContaining('203.0.113.9'), findsWidgets);
    expect(find.text('Отключиться'), findsOneWidget);
    expect(find.text('Подключиться'), findsNothing);

    await process.finish(0);
    await tester.pump();
  });

  testWidgets('пустой терминал подсказывает, что делать', (tester) async {
    _useTallWindow(tester);
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final services = ServicesController(cli: cli);

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.dark(),
        home: Scaffold(body: TunnelPage(tunnel: tunnel, services: services)),
      ),
    );
    await tester.pump();

    expect(find.textContaining('ожидание подключения'), findsOneWidget);
  });

  testWidgets('переключатель системного туннеля меняет состояние контроллера',
      (tester) async {
    _useTallWindow(tester);
    final runner = FakeProcessRunner();
    final cli = fakeCli(runner: runner);
    final tunnel = TunnelController(cli: cli);
    final services = ServicesController(cli: cli);

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.light(),
        home: Scaffold(body: TunnelPage(tunnel: tunnel, services: services)),
      ),
    );
    await tester.pump();

    expect(tunnel.tun, isFalse);
    await tester.tap(find.text('Системный туннель'));
    await tester.pump();

    expect(tunnel.tun, isTrue);
  });
}
