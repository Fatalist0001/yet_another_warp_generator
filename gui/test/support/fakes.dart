/// Фейки для офлайн-тестов: процессы, CLI и пути.
library;

import 'dart:async';
import 'dart:convert';

import 'package:warpgen_gui/src/platform/process_runner.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';
import 'package:warpgen_gui/src/services/warpgen_cli.dart';

/// Один запуск, который сделал GUI.
class FakeInvocation {
  const FakeInvocation(this.executable, this.arguments);

  final String executable;
  final List<String> arguments;

  @override
  String toString() => '$executable ${arguments.join(' ')}';
}

/// Раннер, отдающий заранее подготовленные процессы.
class FakeProcessRunner implements ProcessRunner {
  final List<FakeInvocation> invocations = [];
  final List<FakeProcess> _pending = [];

  /// Готовит процесс для следующего запуска.
  FakeProcess enqueue() {
    final process = FakeProcess();
    _pending.add(process);
    return process;
  }

  @override
  Future<RunningProcess> start(
    String executable,
    List<String> arguments, {
    String? workingDirectory,
    Map<String, String>? environment,
  }) async {
    invocations.add(FakeInvocation(executable, arguments));
    if (_pending.isEmpty) {
      throw StateError(
        'неожиданный запуск: $executable ${arguments.join(' ')}',
      );
    }
    return _pending.removeAt(0);
  }
}

/// Поддельный процесс: тест сам решает, что он напечатает и как завершится.
class FakeProcess implements RunningProcess {
  final _stdout = StreamController<String>.broadcast();
  final _stderr = StreamController<String>.broadcast();
  final _exit = Completer<int>();

  bool interrupted = false;
  bool stopRequested = false;
  bool killed = false;

  @override
  Stream<String> get stdout => _stdout.stream;

  @override
  Stream<String> get stderr => _stderr.stream;

  @override
  Future<int> get exitCode => _exit.future;

  @override
  void sendInterrupt() => interrupted = true;

  @override
  void requestStop() => stopRequested = true;

  @override
  void kill() => killed = true;

  /// Печатает событие пайплайна (NDJSON).
  void emitEvent(Map<String, Object?> event) => _stdout.add(jsonEncode(event));

  /// Печатает произвольную строку stdout.
  void emitLine(String line) => _stdout.add(line);

  /// Печатает строку диагностики (stderr).
  void emitDiagnostic(String line) => _stderr.add(line);

  /// Завершает процесс с кодом.
  Future<void> finish([int code = 0]) async {
    await _stdout.close();
    await _stderr.close();
    if (!_exit.isCompleted) _exit.complete(code);
  }
}

/// CLI с фейковым раннером — тот же код, что в приложении, но без процессов.
WarpgenCli fakeCli({
  required FakeProcessRunner runner,
  String binaryPath = '/opt/warpgen/warpgen',
  String accountFile = '/opt/warpgen/warpgen-account.json',
  String? cacheDir,
  String? elevDir,
  String? stopFile,
  String? stateDir,
  String? endpointCache,
  bool? softStopByFile,
}) {
  return WarpgenCli(
    runner: runner,
    location: WarpgenLocation(
      binaryPath: binaryPath,
      accountFile: accountFile,
      source: 'тест',
    ),
    cacheDir: cacheDir,
    elevDir: elevDir,
    stopFile: stopFile,
    stateDir: stateDir,
    endpointCache: endpointCache,
    softStopByFile: softStopByFile,
  );
}

/// Даёт отработать микрозадачам и потокам событий.
Future<void> flushEvents([int rounds = 3]) async {
  for (var i = 0; i < rounds; i++) {
    await Future<void>.delayed(Duration.zero);
  }
}
