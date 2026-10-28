import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app.dart';
import 'package:warpgen_gui/src/platform/tray_channel.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';

import 'support/fakes.dart';

/// «Выход» из трея: приложение обязано закрыться, а повторный клик — не
/// запускать выход дважды. Трей есть только в Windows-runner'е, поэтому тест
/// пропускается на других платформах.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
  const codec = StandardMethodCodec();
  const channel = MethodChannel(TrayChannel.channelName);

  late List<String> methods;
  late Directory home;

  setUp(() {
    methods = <String>[];
    messenger.setMockMethodCallHandler(channel, (call) async {
      methods.add(call.method);
      return null;
    });
    home = Directory.systemTemp.createTempSync('warpgen-tray');
  });

  tearDown(() {
    messenger.setMockMethodCallHandler(channel, null);
    if (home.existsSync()) home.deleteSync(recursive: true);
  });

  Future<void> fromRunner(WidgetTester tester, String method) async {
    await messenger.handlePlatformMessage(
      TrayChannel.channelName,
      codec.encodeMethodCall(MethodCall(method)),
      (_) {},
    );
    await tester.pumpAndSettle();
  }

  Future<void> pumpApp(WidgetTester tester) async {
    final environment = {
      'PATH': '',
      'HOME': home.path,
      'WARPGEN_BIN': '${home.path}/warpgen',
    };
    await tester.pumpWidget(
      WarpgenApp(
        locator: WarpgenLocator(
          environment: environment,
          workingDirectory: home,
          fileExists: (path) => true,
        ),
        runner: FakeProcessRunner(),
        environment: environment,
      ),
    );
    await tester.runAsync(() async {
      await Future<void>.delayed(const Duration(milliseconds: 100));
    });
    await tester.pumpAndSettle();
  }

  testWidgets(
    'trayQuit закрывает приложение через канал',
    (tester) async {
      await pumpApp(tester);

      await fromRunner(tester, 'trayQuit');

      expect(methods, ['quit']);
      // Интерфейс остаётся рабочим до фактического закрытия окна runner'ом.
      expect(find.text('Подключиться'), findsOneWidget);
    },
    skip: !Platform.isWindows,
  );

  testWidgets(
    'повторный trayQuit не запускает выход дважды',
    (tester) async {
      await pumpApp(tester);

      await fromRunner(tester, 'trayQuit');
      await fromRunner(tester, 'trayQuit');

      expect(methods, ['quit']);
    },
    skip: !Platform.isWindows,
  );
}
