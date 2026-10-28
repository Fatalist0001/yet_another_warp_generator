import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/platform/tray_channel.dart';

/// Канал трея: Dart отправляет команды в runner и принимает команды меню.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
  const codec = StandardMethodCodec();
  const channel = MethodChannel(TrayChannel.channelName);

  late List<String> methods;

  setUp(() {
    methods = <String>[];
    messenger.setMockMethodCallHandler(channel, (call) async {
      methods.add(call.method);
      return null;
    });
  });

  tearDown(() {
    messenger.setMockMethodCallHandler(channel, null);
  });

  /// Имитирует вызов из runner'а в Dart (пункт меню трея).
  Future<void> fromRunner(String method) async {
    await messenger.handlePlatformMessage(
      TrayChannel.channelName,
      codec.encodeMethodCall(MethodCall(method)),
      (_) {},
    );
  }

  test('quit отправляет команду выхода в runner', () async {
    await TrayChannel(supported: true).quit();

    expect(methods, ['quit']);
  });

  test('без трея на платформе команды не отправляются', () async {
    final tray = TrayChannel(supported: false);
    await tray.quit();
    await tray.minimizeToTray();
    await tray.restoreFromTray();

    expect(methods, isEmpty);
  });

  test('trayQuit и trayStopTunnel доходят до обработчиков', () async {
    var quit = 0;
    var stop = 0;
    final tray = TrayChannel(supported: true)
      ..setupTrayHandlers(
        onQuit: () async => quit++,
        onStop: () async => stop++,
      );
    addTearDown(tray.dispose);

    await fromRunner('trayQuit');
    await fromRunner('trayStopTunnel');
    await fromRunner('trayStopTunnel');

    expect(quit, 1);
    expect(stop, 2);
  });

  test('неизвестная команда меню игнорируется', () async {
    var quit = 0;
    final tray = TrayChannel(supported: true)
      ..setupTrayHandlers(onQuit: () async => quit++);
    addTearDown(tray.dispose);

    await fromRunner('traySomethingElse');

    expect(quit, 0);
  });

  test('отсутствующий плагин не мешает выходу', () async {
    messenger.setMockMethodCallHandler(channel, null);

    await expectLater(TrayChannel(supported: true).quit(), completes);
  });
}
