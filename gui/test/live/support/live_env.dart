/// Живое окружение для e2e-тестов GUI: настоящий бинарник `warpgen`,
/// настоящий аккаунт, настоящие процессы и настоящая сеть.
///
/// Здесь нет фейков: тесты в `test/live/` запускают тот же CLI, что и
/// пользователь, и проверяют результат по файлам, событиям и прокси.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/platform/process_runner.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';
import 'package:warpgen_gui/src/services/warpgen_cli.dart';

/// Найденный бинарник, аккаунт и каталог для временных файлов.
class LiveEnv {
  LiveEnv({
    required this.repoRoot,
    required this.binaryPath,
    required this.accountFile,
    required this.workDir,
  });

  final Directory repoRoot;
  final String binaryPath;

  /// Аккаунт WARP для чтения и запуска туннеля (реальный, не фейк).
  final String accountFile;

  /// Временный каталог прогона: конфиги, identity warp-plus, кэш Psiphon.
  final Directory workDir;

  /// CLI, который запускает настоящие процессы.
  WarpgenCli cli({String? accountFile, String? cacheDir}) {
    return WarpgenCli(
      runner: const IoProcessRunner(),
      location: WarpgenLocation(
        binaryPath: binaryPath,
        accountFile: accountFile ?? this.accountFile,
        source: 'live',
      ),
      cacheDir: cacheDir,
    );
  }

  /// Окружение для `WarpgenApp`: путь к бинарнику, аккаунт и изолированный HOME.
  Map<String, String> get environment => {
        'PATH': '',
        'HOME': workDir.path,
        'WARPGEN_BIN': binaryPath,
        'WARPGEN_ACCOUNT': accountFile,
      };
}

LiveEnv? _cached;

/// Готовит (один раз на процесс теста) живое окружение.
Future<LiveEnv> liveEnv() async {
  final cached = _cached;
  if (cached != null) return cached;

  final repoRoot = _findRepoRoot();
  final binary = _findBinary(repoRoot);
  final workDir = await Directory.systemTemp.createTemp('warpgen-gui-live');
  final account = await _resolveAccount(repoRoot, binary, workDir);

  return _cached = LiveEnv(
    repoRoot: repoRoot,
    binaryPath: binary,
    accountFile: account,
    workDir: workDir,
  );
}

/// Корень репозитория: `flutter test` запускается из `gui/`, но тесты можно
/// звать и из корня, поэтому ищем вверх по каталогам.
Directory _findRepoRoot() {
  var dir = Directory.current.absolute;
  for (var depth = 0; depth < 6; depth++) {
    if (File('${dir.path}/go.mod').existsSync() &&
        File('${dir.path}/cmd/warpgen/main.go').existsSync()) {
      return dir;
    }
    final parent = dir.parent;
    if (parent.path == dir.path) break;
    dir = parent;
  }
  fail(
    'не нашли корень репозитория warpgen (go.mod + cmd/warpgen) от '
    '${Directory.current.path}: запускайте тесты из репозитория',
  );
}

String _findBinary(Directory repoRoot) {
  final fromEnv = Platform.environment['WARPGEN_BIN'];
  if (fromEnv != null && fromEnv.trim().isNotEmpty) {
    if (!File(fromEnv.trim()).existsSync()) {
      fail('WARPGEN_BIN=${fromEnv.trim()} не существует');
    }
    return fromEnv.trim();
  }
  final local = File('${repoRoot.path}/warpgen');
  if (!local.existsSync()) {
    fail(
      'нет бинарника ${local.path}: выполните `make build` — '
      'live-тесты GUI запускают настоящий CLI',
    );
  }
  return local.path;
}

/// Аккаунт берём готовый (репозиторий или WARPGEN_ACCOUNT). Если его нет,
/// регистрируем одноразовый через тот же CLI — тогда suite самодостаточен.
Future<String> _resolveAccount(
  Directory repoRoot,
  String binary,
  Directory workDir,
) async {
  final fromEnv = Platform.environment['WARPGEN_ACCOUNT'];
  if (fromEnv != null && fromEnv.trim().isNotEmpty) {
    if (!File(fromEnv.trim()).existsSync()) {
      fail('WARPGEN_ACCOUNT=${fromEnv.trim()} не существует');
    }
    return fromEnv.trim();
  }

  for (final candidate in [
    File('${repoRoot.path}/warpgen-account.json'),
    File('${File(binary).parent.path}/warpgen-account.json'),
  ]) {
    if (candidate.existsSync()) return candidate.path;
  }

  final path = '${workDir.path}/warpgen-account.json';
  final cli = WarpgenCli(
    runner: const IoProcessRunner(),
    location: WarpgenLocation(
      binaryPath: binary,
      accountFile: path,
      source: 'live',
    ),
  );
  await cli.register(fresh: true);
  return path;
}

/// Свободный TCP-порт на loopback (для SOCKS5 GUI).
Future<int> freePort() async {
  final socket = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
  final port = socket.port;
  await socket.close();
  return port;
}

/// Ждёт выполнения условия в реальном времени (тесты с настоящими процессами).
Future<void> eventually(
  bool Function() condition, {
  required Duration timeout,
  required String what,
  Duration step = const Duration(milliseconds: 200),
}) async {
  final deadline = DateTime.now().add(timeout);
  while (DateTime.now().isBefore(deadline)) {
    if (condition()) return;
    await Future<void>.delayed(step);
  }
  fail('не дождались за ${timeout.inSeconds} с: $what');
}

/// Делает `HEAD /` через SOCKS5 и возвращает первые байты ответа.
///
/// Это проверка «прокси действительно работает»: тот же путь, которым пойдёт
/// браузер пользователя, без TLS-обвязки, чтобы тест не зависел от сертификатов.
Future<String> socksHead(
  String socksAddress, {
  String host = 'www.cloudflare.com',
  int port = 80,
  Duration timeout = const Duration(seconds: 20),
}) async {
  final separator = socksAddress.lastIndexOf(':');
  if (separator <= 0) fail('некорректный адрес SOCKS5: $socksAddress');
  final socket = await Socket.connect(
    socksAddress.substring(0, separator),
    int.parse(socksAddress.substring(separator + 1)),
    timeout: timeout,
  );
  final reader = _SocketReader(socket);
  try {
    // Приветствие: версия 5, один метод — без аутентификации.
    socket.add(const [0x05, 0x01, 0x00]);
    final greeting = await reader.take(2);
    expect(greeting, [0x05, 0x00], reason: 'SOCKS5 не принял подключение');

    // CONNECT по доменному имени.
    final hostBytes = ascii.encode(host);
    socket.add([
      0x05,
      0x01,
      0x00,
      0x03,
      hostBytes.length,
      ...hostBytes,
      (port >> 8) & 0xff,
      port & 0xff,
    ]);
    final reply = await reader.take(4);
    expect(reply[0], 0x05, reason: 'ответ SOCKS5 не той версии');
    expect(
      reply[1],
      0x00,
      reason: 'SOCKS5 отклонил соединение (код ${reply[1]})',
    );
    switch (reply[3]) {
      case 0x01:
        await reader.take(4 + 2);
      case 0x04:
        await reader.take(16 + 2);
      case 0x03:
        final length = (await reader.take(1)).first;
        await reader.take(length + 2);
      default:
        fail('неизвестный тип адреса в ответе SOCKS5: ${reply[3]}');
    }

    socket.add(
      ascii.encode(
        'HEAD / HTTP/1.1\r\nHost: $host\r\n'
        'User-Agent: warpgen-gui-e2e\r\nConnection: close\r\n\r\n',
      ),
    );
    return latin1.decode(await reader.take(5));
  } finally {
    await reader.close();
    socket.destroy();
  }
}

/// Читает поток сокета ровно по N байт, сохраняя «лишние» байты.
class _SocketReader {
  _SocketReader(Stream<List<int>> stream)
      : _iterator = StreamIterator<List<int>>(stream);

  final StreamIterator<List<int>> _iterator;
  final List<int> _buffer = [];

  Future<List<int>> take(int count) async {
    while (_buffer.length < count) {
      if (!await _iterator.moveNext()) {
        fail('SOCKS5: соединение закрыто раньше ответа');
      }
      _buffer.addAll(_iterator.current);
    }
    final result = _buffer.sublist(0, count);
    _buffer.removeRange(0, count);
    return result;
  }

  Future<void> close() => _iterator.cancel();
}
