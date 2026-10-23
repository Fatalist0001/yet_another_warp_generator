// Живой туннель через контроллер GUI: настоящий `warpgen run`, настоящий
// SOCKS5, настоящая остановка по SIGINT.
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/controllers/tunnel_controller.dart';

import 'support/live_env.dart';

void main() {
  test('WARP: ready, рабочий SOCKS5 и чистая остановка', () async {
    final env = await liveEnv();
    final port = await freePort();
    final cli = env.cli(cacheDir: '${env.workDir.path}/chain-warp');
    final tunnel = TunnelController(cli: cli);
    addTearDown(tunnel.dispose);
    tunnel.setBind('127.0.0.1:$port');

    await tunnel.start();
    await eventually(
      () =>
          tunnel.status == TunnelStatus.live ||
          tunnel.status == TunnelStatus.failed,
      timeout: const Duration(seconds: 180),
      what: 'пайплайн не дошёл до ready',
    );
    expect(
      tunnel.status,
      TunnelStatus.live,
      reason: tunnel.failure ?? tunnel.detail,
    );

    // ready несёт адрес прокси; порт — тот, что заказал GUI.
    expect(tunnel.socks, '127.0.0.1:$port');
    expect(tunnel.proxyAddress, 'socks5h://127.0.0.1:$port');
    expect(tunnel.endpoint, matches(r'^\d+\.\d+\.\d+\.\d+:\d+$'));
    expect(tunnel.rttMs, greaterThan(0));
    expect(tunnel.journal, isNotEmpty);
    expect(
      tunnel.journal.any((entry) => entry.text.contains('выбран endpoint')),
      isTrue,
      reason: 'в журнале нет события выбора endpoint',
    );

    // Прокси действительно проводит TCP-трафик.
    expect(await socksHead('127.0.0.1:$port'), 'HTTP/');

    await tunnel.stop();
    await eventually(
      () => tunnel.status == TunnelStatus.idle,
      timeout: const Duration(seconds: 30),
      what: 'туннель не остановился по SIGINT',
    );
    expect(tunnel.failure, isNull);
  }, timeout: const Timeout(Duration(minutes: 5)));

  test('WARP + Psiphon: страна NL и рабочий SOCKS5', () async {
    final env = await liveEnv();
    final port = await freePort();
    final cli = env.cli(cacheDir: '${env.workDir.path}/chain-psiphon');
    final tunnel = TunnelController(cli: cli);
    addTearDown(tunnel.dispose);

    // В Psiphon-режиме GUI по умолчанию перечисляет пять стран; для теста
    // оставляем только NL, чтобы подготовка была предсказуемой.
    tunnel.setWarpOnly(false);
    for (final code
        in tunnel.countries.where((code) => code != 'NL').toList()) {
      tunnel.toggleCountry(code);
    }
    expect(tunnel.countries, ['NL']);
    tunnel.setBind('127.0.0.1:$port');

    await tunnel.start();
    await eventually(
      () =>
          tunnel.status == TunnelStatus.live ||
          tunnel.status == TunnelStatus.failed,
      timeout: const Duration(minutes: 4),
      what: 'WARP + Psiphon не дошёл до ready',
    );
    expect(
      tunnel.status,
      TunnelStatus.live,
      reason: tunnel.failure ?? tunnel.detail,
    );

    expect(tunnel.country, 'NL');
    expect(tunnel.ip, isNotNull);
    expect(tunnel.ip, isNotEmpty);
    expect(tunnel.socks, '127.0.0.1:$port');

    expect(await socksHead('127.0.0.1:$port'), 'HTTP/');

    await tunnel.stop();
    await eventually(
      () => tunnel.status == TunnelStatus.idle,
      timeout: const Duration(seconds: 30),
      what: 'цепочка не остановилась по SIGINT',
    );
    expect(tunnel.failure, isNull);
  }, timeout: const Timeout(Duration(minutes: 8)));
}
