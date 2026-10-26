/// Запуск процессов на Android.
///
/// Короткие команды (`register`, `dns-presets`, `services`) по-прежнему идут
/// через `dart:io` в упакованный `libwarpgen.so`. Команда `run --tun` уходит в
/// `VpnService`: Kotlin создаёт TUN, запускает тот же CLI и передаёт ему fd, а
/// события возвращает через `EventChannel`.
library;

import 'dart:async';

import 'android_platform.dart';
import 'process_runner.dart';

/// Раннер для Android: TUN — через VpnService, остальное — обычный процесс.
class AndroidProcessRunner implements ProcessRunner {
  const AndroidProcessRunner();

  static const IoProcessRunner _io = IoProcessRunner();

  @override
  Future<RunningProcess> start(
    String executable,
    List<String> arguments, {
    String? workingDirectory,
    Map<String, String>? environment,
  }) {
    if (arguments.contains('--tun')) {
      return AndroidVpnProcess.start(arguments);
    }
    return _io.start(
      executable,
      arguments,
      workingDirectory: workingDirectory,
      environment: environment,
    );
  }
}

/// Процесс, которым управляет `WarpgenVpnService`.
class AndroidVpnProcess implements RunningProcess {
  AndroidVpnProcess._();

  final StreamController<String> _stdout = StreamController<String>.broadcast();
  final StreamController<String> _stderr = StreamController<String>.broadcast();
  final Completer<int> _exit = Completer<int>();
  StreamSubscription<Map<String, Object?>>? _subscription;
  bool _done = false;

  static Future<AndroidVpnProcess> start(List<String> args) async {
    final process = AndroidVpnProcess._();
    // Подписку оформляем до startVpn: сервис может успеть прислать первые
    // строки сразу после establish().
    process._subscription = AndroidPlatform.events.listen(
      process._onEvent,
      onError: (Object error, StackTrace stack) {
        process._stderr.add('ошибка Android-событий: $error');
        process._finish(1);
      },
    );
    try {
      await AndroidPlatform.startVpn(args);
    } catch (error) {
      process._stderr.add('не удалось запустить VPN: $error');
      process._finish(1);
      rethrow;
    }
    return process;
  }

  void _onEvent(Map<String, Object?> event) {
    switch (event['type']) {
      case 'stdout':
        final line = event['line'];
        if (line is String) _stdout.add(line);
      case 'stderr':
        final line = event['line'];
        if (line is String) _stderr.add(line);
      case 'exit':
        final code = event['code'];
        _finish(code is num ? code.toInt() : 1);
      case 'error':
        final message = event['message'];
        if (message is String) _stderr.add(message);
        _finish(1);
    }
  }

  void _finish(int code) {
    if (_done) return;
    _done = true;
    unawaited(_subscription?.cancel());
    unawaited(_stdout.close());
    unawaited(_stderr.close());
    if (!_exit.isCompleted) _exit.complete(code);
  }

  @override
  Stream<String> get stdout => _stdout.stream;

  @override
  Stream<String> get stderr => _stderr.stream;

  @override
  Future<int> get exitCode => _exit.future;

  @override
  void sendInterrupt() {
    unawaited(AndroidPlatform.stopVpn());
  }

  @override
  void requestStop() {
    unawaited(AndroidPlatform.stopVpn());
  }

  @override
  void kill() {
    unawaited(AndroidPlatform.stopVpn(force: true));
  }
}
