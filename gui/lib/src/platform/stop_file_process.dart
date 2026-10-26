/// Остановка процесса файлом-сигналом `--stop-file`.
///
/// Нужна там, где SIGINT до процесса не доходит:
/// - Linux + `pkexec`: root-процесс принадлежит другому uid, и непривилеженный
///   GUI получает `EPERM`;
/// - Windows: `dart:io` не умеет доставлять сигналы дочернему процессу —
///   `Process.kill` вызывает `TerminateProcess` и убивает CLI мгновенно, без
///   остановки туннеля и дочерних процессов.
///
/// Поэтому GUI создаёт файл, а CLI с `--stop-file` замечает его и завершает
/// пайплайн сам. `kill` остаётся запасным вариантом по таймауту.
library;

import 'dart:io';

import 'process_runner.dart';

/// Обёртка над процессом: `requestStop` пишет файл, который читает CLI.
class StopFileRunningProcess implements RunningProcess {
  StopFileRunningProcess(
    this._inner, {
    required this.stopFile,
    this.signalAfterWrite = false,
  });

  final RunningProcess _inner;

  /// Путь к файлу-сигналу; тот же, что передан CLI в `--stop-file`.
  final String stopFile;

  /// Дублировать остановку сигналом.
  ///
  /// Для `pkexec` сигнал всё равно не дойдёт, но вреда нет; на Windows он
  /// означал бы жёсткое убийство, поэтому там это `false`.
  final bool signalAfterWrite;

  @override
  Stream<String> get stdout => _inner.stdout;

  @override
  Stream<String> get stderr => _inner.stderr;

  @override
  Future<int> get exitCode => _inner.exitCode;

  @override
  void sendInterrupt() => requestStop();

  @override
  void requestStop() {
    File(stopFile).writeAsStringSync('stop');
    if (signalAfterWrite) _inner.sendInterrupt();
  }

  @override
  void kill() => _inner.kill();
}
