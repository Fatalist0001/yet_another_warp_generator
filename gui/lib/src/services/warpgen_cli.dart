/// Обёртка над CLI `warpgen`: единственный источник данных для GUI.
///
/// GUI ничего не знает о внутренностях ядра, он запускает те же команды, что и
/// пользователь в терминале, и разбирает их машинный вывод (`--json`).
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import '../models/scan_report.dart';
import '../platform/process_runner.dart';
import '../platform/warpgen_locator.dart';

/// Ошибка выполнения команды: сообщение уже готово к показу пользователю.
class WarpgenException implements Exception {
  const WarpgenException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// Данные аккаунта WARP (без приватного ключа — он не нужен интерфейсу).
class AccountInfo {
  const AccountInfo({
    required this.id,
    required this.ipv4,
    required this.ipv6,
    required this.peerPublicKey,
  });

  factory AccountInfo.fromJson(Map<String, Object?> json) {
    return AccountInfo(
      id: '${json['id'] ?? ''}',
      ipv4: '${json['ipv4'] ?? ''}',
      ipv6: '${json['ipv6'] ?? ''}',
      peerPublicKey: '${json['peer_public_key'] ?? ''}',
    );
  }

  final String id;
  final String ipv4;
  final String ipv6;
  final String peerPublicKey;

  /// Аккаунт заполнен: есть адреса и ключ сервера.
  bool get isComplete =>
      ipv4.trim().isNotEmpty &&
      ipv6.trim().isNotEmpty &&
      peerPublicKey.trim().isNotEmpty;

  /// Короткий идентификатор для интерфейса.
  String get shortId => id.length <= 8 ? id : '${id.substring(0, 8)}…';
}

/// Команды warpgen, нужные интерфейсу.
class WarpgenCli {
  WarpgenCli({required this.runner, required this.location, this.cacheDir});

  final ProcessRunner runner;
  final WarpgenLocation location;

  /// Каталог identity warp-plus (по умолчанию его выбирает сам CLI).
  final String? cacheDir;

  String get binaryPath => location.binaryPath;

  String get accountFile => location.accountFile;

  /// Запускает пайплайн `run` и отдаёт процесс с потоком событий.
  ///
  /// `warpOnly` — режим без Psiphon (SOCKS5 поверх WARP-туннеля).
  /// `tun` — системный туннель: TUN-устройство и маршруты машины, поэтому CLI
  /// должен работать под root и запускается через `pkexec`. В этом режиме
  /// `--cache-dir` передаётся всегда: под root переменная `HOME` указывает на
  /// каталог администратора, и без явного пути identity warp-plus уедет туда.
  Future<RunningProcess> startRun({
    required bool warpOnly,
    required List<String> countries,
    required String bind,
    bool tun = false,
  }) {
    final command = <String>[
      if (tun) 'pkexec',
      binaryPath,
      'run',
      '--json',
      '--account',
      accountFile,
      '--bind',
      bind,
      if (tun) '--tun',
      if (warpOnly)
        '--warp-only'
      else ...[
        '--countries',
        countries.join(','),
      ],
      if (_filled(cacheDir) && (!warpOnly || tun)) ...[
        '--cache-dir',
        cacheDir!
      ],
    ];
    return runner.start(command.first, command.skip(1).toList());
  }

  /// Сканирует endpoint'ы. Отчёт приходит одним JSON-документом в конце.
  Future<ScanReport> scan({
    int maxCandidates = 256,
    int target = 8,
    bool strictRegion = false,
    void Function(int done, int total)? onProgress,
    void Function(String line)? onDiagnostic,
  }) async {
    final args = <String>[
      'scan',
      '--json',
      '--debug',
      '--max',
      '$maxCandidates',
      '--target',
      '$target',
      '--account',
      accountFile,
      if (strictRegion) '--strict-region',
    ];
    final process = await runner.start(binaryPath, args);

    final out = <String>[];
    final err = <String>[];
    final progressPattern = RegExp(r'проверено\s+(\d+)/(\d+)');

    final stdoutDone = process.stdout.forEach(out.add);
    final stderrDone = process.stderr.forEach((line) {
      final text = line.trim();
      if (text.isEmpty) return;
      err.add(text);
      final match = progressPattern.firstMatch(text);
      if (match != null) {
        onProgress?.call(
          int.parse(match.group(1)!),
          int.parse(match.group(2)!),
        );
        return;
      }
      onDiagnostic?.call(text);
    });

    final code = await process.exitCode;
    await _drain([stdoutDone, stderrDone]);
    if (code != 0) {
      throw WarpgenException(_failure('скан', err));
    }

    try {
      final decoded = jsonDecode(out.join('\n'));
      if (decoded is! Map<String, Object?>) {
        throw const WarpgenException('не удалось разобрать отчёт скана');
      }
      return ScanReport.fromJson(decoded);
    } on FormatException {
      throw const WarpgenException('не удалось разобрать отчёт скана');
    }
  }

  /// Генерирует `.conf` для AmneziaWG и возвращает путь к файлу.
  Future<String> generateConfig({
    required String endpoint,
    required String outputPath,
    String dns = '1.1.1.1',
  }) async {
    final args = <String>[
      'gen',
      '--endpoint',
      endpoint,
      '--output',
      outputPath,
      '--dns',
      dns,
      '--account',
      accountFile,
    ];
    final process = await runner.start(binaryPath, args);
    final out = <String>[];
    final err = <String>[];
    final stdoutDone = process.stdout.forEach(out.add);
    final stderrDone = process.stderr.forEach((line) => err.add(line.trim()));
    final code = await process.exitCode;
    await _drain([stdoutDone, stderrDone]);
    if (code != 0) {
      throw WarpgenException(_failure('генерация конфига', [...err, ...out]));
    }
    return outputPath;
  }

  /// Регистрирует (или перерегистрирует) аккаунт WARP.
  Future<AccountInfo> register({bool fresh = false}) async {
    final args = <String>[
      'register',
      '--json',
      '--account',
      accountFile,
      if (fresh) '--fresh',
    ];
    final process = await runner.start(binaryPath, args);
    final out = <String>[];
    final err = <String>[];
    final stdoutDone = process.stdout.forEach(out.add);
    final stderrDone = process.stderr.forEach((line) => err.add(line.trim()));
    final code = await process.exitCode;
    await _drain([stdoutDone, stderrDone]);
    if (code != 0) {
      throw WarpgenException(_failure('регистрация', err));
    }
    try {
      final decoded = jsonDecode(out.join('\n'));
      if (decoded is! Map<String, Object?>) {
        throw const WarpgenException('не удалось разобрать ответ регистрации');
      }
      return AccountInfo.fromJson(decoded);
    } on FormatException {
      throw const WarpgenException('не удалось разобрать ответ регистрации');
    }
  }

  /// Читает аккаунт с диска, не трогая сеть. `null` — файла нет.
  Future<AccountInfo?> readAccount() async {
    final file = File(accountFile);
    if (!await file.exists()) return null;
    try {
      final decoded = jsonDecode(await file.readAsString());
      if (decoded is! Map<String, Object?>) return null;
      final info = AccountInfo.fromJson(decoded);
      return info.isComplete ? info : null;
    } on FormatException {
      return null;
    } on FileSystemException {
      return null;
    }
  }
}

/// Ждёт закрытия потоков, но не бесконечно: процесс уже завершился, и если
/// труба не закрылась, ждать нечего.
Future<void> _drain(List<Future<void>> streams) async {
  try {
    await Future.wait(streams).timeout(const Duration(seconds: 5));
  } on TimeoutException {
    // Ничего страшного: вывод уже прочитан, что успели.
  }
}

String _failure(String action, List<String> errLines) {
  final tail =
      errLines.where((line) => line.trim().isNotEmpty).toList(growable: false);
  final tailText = tail.isEmpty
      ? ''
      : ': ${tail.length > 3 ? tail.sublist(tail.length - 3).join('; ') : tail.join('; ')}';
  return '$action не удалась$tailText';
}

bool _filled(String? value) => value != null && value.trim().isNotEmpty;
