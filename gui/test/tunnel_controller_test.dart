import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/controllers/tunnel_controller.dart';

import 'support/fakes.dart';

void main() {
  test('события пайплайна переводят состояние в «подключено»', () async {
    final runner = FakeProcessRunner();
    final clock = DateTime(2026, 10, 20, 12, 0, 0);
    final tunnel = TunnelController(
      cli: fakeCli(runner: runner),
      clock: () => clock,
    );
    final process = runner.enqueue();

    await tunnel.start();
    expect(tunnel.status, TunnelStatus.starting);
    expect(tunnel.isRunning, isTrue);

    process
      ..emitEvent(const {
        'event': 'endpoint_selected',
        'endpoint': '162.159.192.1:2408',
        'colo': 'AMS',
        'rtt_ms': 42,
      })
      ..emitEvent(const {'event': 'outer_up', 'endpoint': '162.159.192.1:2408'})
      ..emitEvent(const {'event': 'outer_checked', 'colo': 'AMS'})
      ..emitEvent(const {
        'event': 'forwarder_up',
        'forwarder': '127.0.0.1:5555',
      })
      ..emitEvent(const {'event': 'socks_up', 'socks': '127.0.0.1:8086'})
      ..emitEvent(const {
        'event': 'ready',
        'socks': '127.0.0.1:8086',
        'country': 'NL',
        'ip': '203.0.113.9',
        'org': 'AS1234 Example',
        'seconds': 12,
      });
    await flushEvents();

    expect(tunnel.status, TunnelStatus.live);
    expect(tunnel.socks, '127.0.0.1:8086');
    expect(tunnel.proxyAddress, 'socks5h://127.0.0.1:8086');
    expect(tunnel.country, 'NL');
    expect(tunnel.ip, '203.0.113.9');
    expect(tunnel.colo, 'AMS');
    expect(tunnel.rttMs, 42);
    expect(tunnel.readyAt, clock);
    expect(tunnel.prepareTime, const Duration(seconds: 12));
    expect(tunnel.headline, 'Подключено');
    expect(tunnel.journal, isNotEmpty);
    expect(tunnel.journal.last.text, contains('NL'));

    final stopFuture = tunnel.stop();
    await flushEvents();
    expect(process.interrupted, isTrue);
    expect(tunnel.status, TunnelStatus.stopping);

    await process.finish(0);
    await stopFuture;
    await flushEvents();

    expect(tunnel.status, TunnelStatus.idle);
    expect(tunnel.headline, 'Туннель выключен');
  });

  test('событие stopped с ошибкой и код 1 дают состояние failed', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    final process = runner.enqueue();

    await tunnel.start();
    process.emitEvent(const {
      'event': 'stopped',
      'error': 'нет аккаунта warpgen (warpgen-account.json)',
    });
    await flushEvents();
    await process.finish(1);
    await flushEvents();

    expect(tunnel.status, TunnelStatus.failed);
    expect(tunnel.failure, contains('нет аккаунта warpgen'));
    expect(tunnel.headline, 'Не удалось подключиться');
  });

  test('некорректный адрес SOCKS5 не запускает процесс', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    tunnel.setBind('0.0.0.0:8086');

    expect(tunnel.validateBind(), isNotNull);
    await tunnel.start();

    expect(tunnel.status, TunnelStatus.failed);
    expect(runner.invocations, isEmpty);
  });

  test('адрес SOCKS5 по умолчанию валиден', () {
    final tunnel = TunnelController(cli: fakeCli(runner: FakeProcessRunner()));
    expect(tunnel.bind, '127.0.0.1:8086');
    expect(tunnel.validateBind(), isNull);
    expect(tunnel.warpOnly, isTrue);
    expect(tunnel.countries, ['NL', 'SE', 'DE', 'US', 'PL']);
  });

  test('страны можно включать и выключать, порядок сохраняется', () {
    final tunnel = TunnelController(cli: fakeCli(runner: FakeProcessRunner()));

    tunnel.toggleCountry('NL');
    expect(tunnel.countries, ['SE', 'DE', 'US', 'PL']);

    tunnel.toggleCountry('fi');
    expect(tunnel.countries, ['SE', 'DE', 'US', 'PL', 'FI']);
  });

  test('настройки не меняются во время работы', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    runner.enqueue();

    await tunnel.start();
    tunnel.setWarpOnly(false);
    tunnel.toggleCountry('NL');
    tunnel.setBind('127.0.0.1:9999');

    expect(tunnel.warpOnly, isTrue);
    expect(tunnel.countries, contains('NL'));
    expect(tunnel.bind, '127.0.0.1:8086');
  });

  test('ошибка запуска процесса попадает в состояние', () async {
    final tunnel = TunnelController(cli: fakeCli(runner: FakeProcessRunner()));

    await tunnel.start();

    expect(tunnel.status, TunnelStatus.failed);
    expect(tunnel.failure, contains('неожиданный запуск'));
  });
}
