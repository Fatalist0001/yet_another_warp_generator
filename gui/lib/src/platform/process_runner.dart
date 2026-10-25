/// Запуск внешних процессов warpgen.
///
/// Интерфейс нужен, чтобы контроллеры проверялись без настоящих процессов и
/// сети: в тестах подставляется фейковый раннер (см. `test/support/fakes.dart`).
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

/// Запускает процесс и отдаёт его потоки вывода.
abstract class ProcessRunner {
  Future<RunningProcess> start(
    String executable,
    List<String> arguments, {
    String? workingDirectory,
    Map<String, String>? environment,
  });
}

/// Запущенный процесс: строки stdout/stderr, код возврата и остановка.
abstract class RunningProcess {
  /// Строки stdout (у `run --json` — NDJSON, у остальных команд — один документ).
  Stream<String> get stdout;

  /// Строки stderr — диагностика и прогресс.
  Stream<String> get stderr;

  /// Код возврата процесса.
  Future<int> get exitCode;

  /// Мягкая остановка (SIGINT): Go-пайплайн успевает снять маршруты и ресурсы.
  void sendInterrupt();

  /// Остановка с учётом прав процесса.
  ///
  /// По умолчанию это SIGINT, но процесс под root (`--tun` через pkexec) сигнал
  /// от непривилегированного GUI не получит: там остановка идёт файлом-сигналом.
  void requestStop() => sendInterrupt();

  /// Жёсткая остановка (SIGKILL) — запасной вариант, если SIGINT не помог.
  void kill();
}

/// Реализация на `dart:io`.
class IoProcessRunner implements ProcessRunner {
  const IoProcessRunner();

  @override
  Future<RunningProcess> start(
    String executable,
    List<String> arguments, {
    String? workingDirectory,
    Map<String, String>? environment,
  }) async {
    final process = await Process.start(
      executable,
      arguments,
      workingDirectory: workingDirectory,
      environment: environment,
      runInShell: false,
    );
    return IoRunningProcess(process);
  }
}

/// Обёртка над `Process`, разбивающая вывод на строки.
class IoRunningProcess implements RunningProcess {
  IoRunningProcess(this._process);

  final Process _process;

  @override
  Stream<String> get stdout => _splitLines(_process.stdout);

  @override
  Stream<String> get stderr => _splitLines(_process.stderr);

  @override
  Future<int> get exitCode => _process.exitCode;

  @override
  void sendInterrupt() => _process.kill(ProcessSignal.sigint);

  @override
  void requestStop() => sendInterrupt();

  @override
  void kill() => _process.kill(ProcessSignal.sigkill);
}

/// Делит вывод на строки по `\n` и `\r`.
///
/// Прогресс скана печатается через `\r` (одна строка перерисовывается), а JSON
/// событий — через `\n`, поэтому нужны оба разделителя.
Stream<String> _splitLines(Stream<List<int>> raw) {
  var pending = '';
  return raw.transform(utf8.decoder).transform(
        StreamTransformer<String, String>.fromHandlers(
          handleData: (chunk, sink) {
            pending += chunk;
            var start = 0;
            for (var i = 0; i < pending.length; i++) {
              final char = pending[i];
              if (char == '\n' || char == '\r') {
                if (i > start) sink.add(pending.substring(start, i));
                start = i + 1;
              }
            }
            pending = pending.substring(start);
          },
          handleDone: (sink) {
            if (pending.isNotEmpty) sink.add(pending);
            sink.close();
          },
        ),
      );
}
