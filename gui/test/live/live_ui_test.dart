// Живой интерфейс без скриншотов: настоящее дерево виджетов, настоящий процесс
// `warpgen` и настоящий SOCKS5. Проверяем то, что видит пользователь.
//
// Обычный AutomatedTestWidgetsFlutterBinding держит фейковые часы, и
// `Process.start`, вызванный из обработчика кнопки, не завершается. Живой
// binding идёт в реальном времени — так тест и работает с настоящим CLI.
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app.dart';
import 'package:warpgen_gui/src/platform/process_runner.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';

import 'support/live_env.dart';

void main() {
  LiveTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('главный экран поднимает и останавливает настоящий туннель',
      (tester) async {
    final env = await liveEnv();
    // Порт по умолчанию: как в CLI и в подписи интерфейса.
    const port = 8086;

    tester.view.physicalSize = const Size(900, 1800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    await tester.pumpWidget(
      WarpgenApp(
        locator: WarpgenLocator(
          environment: env.environment,
          workingDirectory: Directory(env.workDir.path),
        ),
        runner: const IoProcessRunner(),
        environment: env.environment,
      ),
    );

    await _pumpUntil(
      tester,
      () => find.text('Подключиться').evaluate().isNotEmpty,
      what: 'кнопка подключения не появилась',
    );

    // Даём маршруту доанимироваться: во время перехода он перехватывает нажатия.
    await Future<void>.delayed(const Duration(milliseconds: 800));
    await tester.pump();

    final connect = find.widgetWithText(FilledButton, 'Подключиться');
    expect(connect, findsOneWidget);
    expect(
      tester.widget<FilledButton>(connect).onPressed,
      isNotNull,
      reason: 'кнопка подключения отключена',
    );
    await tester.tap(connect);
    await tester.pump();

    try {
      await _pumpUntil(
        tester,
        () =>
            find.text('Подключено').evaluate().isNotEmpty ||
            find.text('Не удалось подключиться').evaluate().isNotEmpty,
        timeout: const Duration(seconds: 120),
        what: 'туннель не дошёл до готовности',
      );

      expect(
        find.text('Не удалось подключиться'),
        findsNothing,
        reason: 'GUI показал ошибку подключения',
      );
      expect(find.text('Подключено'), findsOneWidget);
      expect(find.text('Отключиться'), findsOneWidget);
      expect(find.text('Подключиться'), findsNothing);
      expect(find.text('socks5h://127.0.0.1:$port'), findsOneWidget);

      // Прокси, который показал интерфейс, действительно проводит трафик.
      expect(await socksHead('127.0.0.1:$port'), 'HTTP/');
    } finally {
      final disconnect = find.widgetWithText(FilledButton, 'Отключиться');
      if (disconnect.evaluate().isNotEmpty) {
        await tester.tap(disconnect);
        await tester.pump();
      }
    }

    await _pumpUntil(
      tester,
      () => find.text('Туннель выключен').evaluate().isNotEmpty,
      timeout: const Duration(seconds: 40),
      what: 'туннель не остановился',
    );
    expect(find.text('Отключиться'), findsNothing);
    expect(find.text('Подключиться'), findsOneWidget);

    // Закрываем приложение: контроллер не должен оставить процесс.
    await tester.pumpWidget(const SizedBox());
    await tester.pump();
  }, timeout: const Timeout(Duration(minutes: 6)));
}

/// Крутит реальное время и кадры, пока условие не выполнится.
Future<void> _pumpUntil(
  WidgetTester tester,
  bool Function() done, {
  required String what,
  Duration timeout = const Duration(seconds: 60),
  Duration step = const Duration(milliseconds: 200),
}) async {
  final deadline = DateTime.now().add(timeout);
  while (DateTime.now().isBefore(deadline)) {
    await Future<void>.delayed(step);
    await tester.pump();
    if (done()) return;
  }
  final texts = tester
      .widgetList<Text>(find.byType(Text))
      .map((text) => text.data)
      .whereType<String>()
      .toList();
  fail('не дождались за ${timeout.inSeconds} с: $what; на экране: $texts');
}
