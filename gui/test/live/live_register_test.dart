// Живая регистрация WARP через настоящий CLI.
//
// Прямой API Cloudflare из России недоступен, поэтому CLI регистрирует через
// туннель по существующему аккаунту — ровно этот путь и проверяем.
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'support/live_env.dart';

void main() {
  test('register: новый аккаунт через туннель и чтение с диска', () async {
    final env = await liveEnv();
    final dir = await Directory.systemTemp.createTemp('warpgen-gui-register');
    addTearDown(() => dir.delete(recursive: true));
    final accountFile = '${dir.path}/warpgen-account.json';
    // Копия рабочего аккаунта: CLI использует её ключи для входа в туннель,
    // а результат пишет уже в новый файл (репозиторный аккаунт не трогаем).
    File(env.accountFile).copySync(accountFile);
    final before = await env.cli(accountFile: accountFile).readAccount();
    expect(before, isNotNull);

    final cli = env.cli(accountFile: accountFile);
    final fresh = await cli.register(fresh: true);
    expect(fresh.isComplete, isTrue);
    expect(fresh.id, isNotEmpty);
    expect(fresh.id, isNot(before!.id), reason: 'аккаунт не перерегистрирован');
    expect(fresh.ipv4, isNotEmpty);
    expect(fresh.ipv6, contains(':'));
    expect(fresh.peerPublicKey, isNotEmpty);

    // Без --fresh CLI переиспользует сохранённый аккаунт.
    final again = await cli.register();
    expect(again.id, fresh.id);

    final fromDisk = await cli.readAccount();
    expect(fromDisk, isNotNull);
    expect(fromDisk!.id, fresh.id);
    expect(fromDisk.shortId, endsWith('…'));
  }, timeout: const Timeout(Duration(minutes: 4)));
}
