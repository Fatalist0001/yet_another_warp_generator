/// Остановка процесса, запущенного через `pkexec` под root.
///
/// `pkexec` делает `exec` и сам становится `warpgen` с uid 0, а непривилеженный
/// GUI не может послать ему сигнал (`EPERM`). Поэтому мягкая остановка идёт
/// файлом-сигналом: CLI видит `--stop-file`, сам завершает пайплайн и успевает
/// снять TUN и маршруты. SIGKILL для root-процесса тоже недоступен.
library;

import 'dart:io';

import 'process_runner.dart';

/// Обёртка над root-процессом: `requestStop` пишет файл, который читает CLI.
class ElevatedRunningProcess implements RunningProcess {
  ElevatedRunningProcess(this._inner, {required this.stopFile});

  final RunningProcess _inner;

  /// Путь к файлу-сигналу; тот же, что передан CLI в `--stop-file`.
  final String stopFile;

  @override
  Stream<String> get stdout => _inner.stdout;

  @override
  Stream<String> get stderr => _inner.stderr;

  @override
  Future<int> get exitCode => _inner.exitCode;

  /// Даже прямой SIGINT бесполезен для root — ведём себя как `requestStop`.
  @override
  void sendInterrupt() => requestStop();

  @override
  void requestStop() {
    File(stopFile).writeAsStringSync('stop');
    _inner.sendInterrupt();
  }

  @override
  void kill() => _inner.kill();
}
