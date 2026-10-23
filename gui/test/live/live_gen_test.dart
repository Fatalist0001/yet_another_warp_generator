// Живые проверки генерации конфига и аккаунта: настоящий CLI, настоящий
// хендшейк с Cloudflare (без `--no-verify`), настоящий файл `.conf`.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/services/warpgen_cli.dart';

import 'support/live_env.dart';

void main() {
  test('generateConfig: хендшейк, файл и WARP-safe значения', () async {
    final env = await liveEnv();
    final cli = env.cli();
    final output = File('${env.workDir.path}/warp-live.conf');

    final path = await cli.generateConfig(
      endpoint: '162.159.192.1:2408',
      outputPath: output.path,
      dns: '1.1.1.1',
    );

    expect(path, output.path);
    expect(output.existsSync(), isTrue, reason: 'CLI не создал конфиг');

    final conf = output.readAsStringSync();
    final interface = _section(conf, 'Interface');
    final peer = _section(conf, 'Peer');

    // Инварианты WARP-safe из AGENTS.md: сервер Cloudflare не знает про AmneziaWG.
    expect(interface, contains('MTU = 1280'));
    expect(interface, contains('DNS = 1.1.1.1'));
    for (final field in ['S1', 'S2', 'S3', 'S4']) {
      expect(interface, contains('$field = 0'));
    }
    for (var i = 1; i <= 4; i++) {
      expect(interface, contains('H$i = $i'));
    }
    expect(interface, contains(RegExp(r'^I1 = ', multiLine: true)));

    expect(peer, contains('Endpoint = 162.159.192.1:2408'));
    expect(peer, contains('PersistentKeepalive = 25'));
    expect(peer, contains('AllowedIPs = 0.0.0.0/0, ::/0'));
    expect(interface, isNot(contains('PersistentKeepalive')));
  }, timeout: const Timeout(Duration(minutes: 2)));

  test('generateConfig: недоступный endpoint — понятная ошибка', () async {
    final env = await liveEnv();
    final cli = env.cli();
    final output = File('${env.workDir.path}/warp-dead.conf');

    await expectLater(
      cli.generateConfig(
        endpoint: '127.0.0.1:1',
        outputPath: output.path,
      ),
      throwsA(isA<WarpgenException>()),
    );
    expect(output.existsSync(), isFalse, reason: 'конфиг не должен появиться');
  }, timeout: const Timeout(Duration(minutes: 2)));

  test('readAccount: реальный файл аккаунта читается', () async {
    final env = await liveEnv();
    final account = await env.cli().readAccount();

    expect(account, isNotNull);
    expect(account!.isComplete, isTrue);
    expect(account.ipv4, isNotEmpty);
    expect(account.ipv6, contains(':'));
    expect(account.peerPublicKey, isNotEmpty);
  });
}

// Возвращает строки одной секции конфига без заголовка.
String _section(String conf, String name) {
  final lines = conf.split('\n');
  final start = lines.indexWhere((line) => line.trim() == '[$name]');
  if (start < 0) fail('в конфиге нет секции [$name]');
  final body = <String>[];
  for (final line in lines.skip(start + 1)) {
    if (line.trim().startsWith('[')) break;
    body.add(line);
  }
  return body.join('\n');
}
