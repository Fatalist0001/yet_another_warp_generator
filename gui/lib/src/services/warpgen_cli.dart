/// Обёртка над CLI `warpgen`: единственный источник данных для GUI.
///
/// GUI ничего не знает о внутренностях ядра, он запускает те же команды, что и
/// пользователь в терминале, и разбирает их машинный вывод (`--json`).
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import '../models/dns_preset.dart';
import '../models/service_status.dart';
import '../platform/elevated_binary.dart';
import '../platform/process_runner.dart';
import '../platform/stop_file_process.dart';
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
  WarpgenCli({
    required this.runner,
    required this.location,
    this.cacheDir,
    this.elevDir,
    this.stopFile,
    this.stateDir,
    this.endpointCache,
    bool? softStopByFile,
  }) : softStopByFile = softStopByFile ?? Platform.isWindows;

  final ProcessRunner runner;
  final WarpgenLocation location;

  /// Останавливать `run` файлом-сигналом, а не SIGINT.
  ///
  /// На Windows сигнал дочернему процессу не доставить: `Process.kill` — это
  /// `TerminateProcess`, после которого CLI не успевает снять туннель и
  /// дочерние процессы. На Linux остаётся SIGINT (softStopByFile = false).
  final bool softStopByFile;

  /// Каталог identity warp-plus (по умолчанию его выбирает сам CLI).
  final String? cacheDir;

  /// Каталог копии бинарника для root (`--tun`).
  ///
  /// AppImage лежит на FUSE-монтировании, доступном только пользователю, а
  /// `pkexec` проверяет доступ до запроса пароля. Пусто — запускать как есть
  /// (тесты и окружения, где бинарник и так доступен root).
  final String? elevDir;

  /// Файл-сигнал остановки (`--stop-file`).
  ///
  /// Нужен процессу под root (`pkexec` не получает SIGINT от GUI) и любому
  /// процессу на Windows. GUI создаёт этот файл, а CLI замечает его и
  /// завершает пайплайн сам, успев снять TUN, туннель и дочерние процессы.
  /// Пусто — остановка только сигналом.
  final String? stopFile;

  /// Каталог журнала маршрутов для `tun-revert` (пусто — ~/.warpgen/state).
  ///
  /// Портативная сборка передаёт сюда каталог рядом с exe, чтобы журнал
  /// аварийного отката не оставался в профиле пользователя.
  final String? stateDir;

  /// Файл кэша endpoint'ов, выбранных по скорости при первой настройке.
  ///
  /// Пока файла нет, CLI сканирует endpoint'ы и меряет итоговую скорость; при
  /// следующих подключениях берёт готовый список. «Пересоздать конфигурацию»
  /// удаляет файл, и замер повторяется.
  final String? endpointCache;

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
    List<String> dns = const [],
  }) async {
    final args = <String>[
      'run',
      '--json',
      '--account',
      accountFile,
      '--bind',
      bind,
      if (_filled(endpointCache)) ...['--endpoint-cache', endpointCache!],
      if (_filled(stateDir)) ...['--state-dir', stateDir!],
      if (dns.isNotEmpty) ...['--dns', dns.join(',')],
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

    // Android: TUN создаёт VpnService, а не pkexec. CLI получает fd по
    // unix-сокету от Android-обёртки, поэтому root и polkit здесь не нужны.
    if (Platform.isAndroid && tun) {
      final extra = <String>[];
      final warpPlus = File('${location.binaryDir}/libwarp-plus.so');
      if (await warpPlus.exists()) {
        extra.addAll(['--warp-plus', warpPlus.path]);
      }
      return runner.start(binaryPath, [...args, ...extra]);
    }

    // Windows: GUI запускается с манифестом requireAdministrator, поэтому CLI
    // наследует права и TUN поднимается напрямую, без pkexec. Сигналы
    // дочернему процессу там не доставляются — остановка файлом.
    if (Platform.isWindows) {
      final winStop = await _prepareStopFile();
      if (winStop != null) args.addAll(['--stop-file', winStop]);
      final process = await runner.start(binaryPath, args);
      if (winStop == null) return process;
      return StopFileRunningProcess(process, stopFile: winStop);
    }

    // SIGINT дочернему процессу не доставить — мягкая остановка идёт тем же
    // файлом-сигналом, что и для root под pkexec.
    final stop = (tun || softStopByFile) ? await _prepareStopFile() : null;
    if (stop != null) args.addAll(['--stop-file', stop]);
    if (!tun) {
      final process = await runner.start(binaryPath, args);
      if (stop == null) return process;
      return StopFileRunningProcess(process, stopFile: stop);
    }

    final executable = await _elevatedBinary();
    try {
      final process = await runner.start('pkexec', [executable, ...args]);
      if (stop == null) return process;
      return StopFileRunningProcess(
        process,
        stopFile: stop,
        signalAfterWrite: true,
      );
    } on ProcessException catch (error) {
      throw WarpgenException(
        'pkexec не запустился: ${error.message}. '
        'Системному туннелю нужен polkit (пакет polkitd/policykit-1).',
      );
    }
  }

  /// DNS-пресеты. `resolve` дополнительно резолвит hostname'ы в IP.
  Future<List<DnsPreset>> dnsPresets({bool resolve = false}) async {
    final decoded = await _jsonCommand('список DNS', [
      'dns-presets',
      '--json',
      if (resolve) '--resolve',
    ]);
    if (decoded is! List) {
      throw const WarpgenException('не удалось разобрать список DNS');
    }
    final presets = <DnsPreset>[];
    for (final item in decoded) {
      if (item is Map) {
        presets.add(DnsPreset.fromJson(item.cast<String, Object?>()));
      }
    }
    return presets;
  }

  /// DNS текущего подключения — для пресета «DNS провайдера».
  Future<DnsCurrent> dnsCurrent() async {
    final decoded = await _jsonCommand('определение DNS', [
      'dns-current',
      '--json',
    ]);
    if (decoded is! Map) {
      throw const WarpgenException('не удалось разобрать DNS провайдера');
    }
    return DnsCurrent.fromJson(decoded.cast<String, Object?>());
  }

  /// Проверяет доступность сервисов через SOCKS5.
  Future<ServiceReport> checkServices(String socks) async {
    final decoded = await _jsonCommand('проверка сервисов', [
      'services',
      '--json',
      '--socks',
      socks,
    ]);
    if (decoded is! Map) {
      throw const WarpgenException('не удалось разобрать проверку сервисов');
    }
    return ServiceReport.fromJson(decoded.cast<String, Object?>());
  }

  /// Готовит файл-сигнал остановки: stale-файл от прошлого прогона удаляется,
  /// иначе новый процесс (root или Windows) завершится сразу после старта.
  Future<String?> _prepareStopFile() async {
    final path = stopFile?.trim();
    if (path == null || path.isEmpty) return null;
    try {
      final file = File(path);
      await file.parent.create(recursive: true);
      if (await file.exists()) await file.delete();
    } on FileSystemException catch (error) {
      throw WarpgenException(
        'не удалось подготовить файл остановки: ${error.message}',
      );
    }
    return path;
  }

  /// Путь к бинарнику, который сможет прочитать root.
  Future<String> _elevatedBinary() async {
    final dir = elevDir?.trim();
    if (dir == null || dir.isEmpty) return binaryPath;
    try {
      return await ElevatedBinary(sourcePath: binaryPath, targetDir: dir)
          .prepare();
    } on FileSystemException catch (error) {
      throw WarpgenException(
        'не удалось подготовить копию CLI для root: ${error.message}',
      );
    }
  }

  /// Регистрирует (или перерегистрирует) аккаунт WARP.
  Future<AccountInfo> register({bool fresh = false}) async {
    final decoded = await _jsonCommand('регистрация', [
      'register',
      '--json',
      '--account',
      accountFile,
      if (fresh) '--fresh',
    ]);
    if (decoded is! Map) {
      throw const WarpgenException('не удалось разобрать ответ регистрации');
    }
    final info = AccountInfo.fromJson(decoded.cast<String, Object?>());
    if (fresh) {
      await _clearWarpPlusIdentity();
    }
    return info;
  }

  /// Удаляет identity warp-plus при пересоздании конфигурации: иначе старый
  /// аккаунт продолжит использоваться в режиме с Psiphon.
  Future<void> _clearWarpPlusIdentity() async {
    final dir = cacheDir?.trim();
    if (dir == null || dir.isEmpty) return;
    final file = File('$dir/primary/wgcf-identity.json');
    try {
      if (await file.exists()) await file.delete();
    } on FileSystemException catch (error) {
      throw WarpgenException(
        'не удалось удалить identity warp-plus: ${error.message}',
      );
    }
  }

  /// Удаляет кэш endpoint'ов при пересоздании конфигурации.
  ///
  /// Следующее подключение снова выберет endpoint'ы по скорости. Файла может
  /// не быть — это не ошибка.
  Future<void> clearEndpointCache() async {
    final path = endpointCache?.trim();
    if (path == null || path.isEmpty) return;
    try {
      final file = File(path);
      if (await file.exists()) await file.delete();
    } on FileSystemException catch (error) {
      throw WarpgenException(
        'не удалось удалить кэш endpoint\'ов: ${error.message}',
      );
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

  /// Запускает короткую команду с `--json` и разбирает единственный документ.
  Future<Object?> _jsonCommand(String action, List<String> args) async {
    final process = await runner.start(binaryPath, args);
    final out = <String>[];
    final err = <String>[];
    final stdoutDone = process.stdout.forEach(out.add);
    final stderrDone = process.stderr.forEach((line) {
      final text = line.trim();
      if (text.isNotEmpty) err.add(text);
    });
    final code = await process.exitCode;
    await _drain([stdoutDone, stderrDone]);
    if (code != 0) {
      throw WarpgenException(_failure(action, [...err, ...out]));
    }
    try {
      return jsonDecode(out.join('\n'));
    } on FormatException {
      throw WarpgenException('не удалось разобрать ответ: $action');
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
