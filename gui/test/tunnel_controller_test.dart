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
    expect(process.stopRequested, isTrue);
    expect(tunnel.status, TunnelStatus.stopping);

    await process.finish(0);
    await stopFuture;
    await flushEvents();

    expect(tunnel.status, TunnelStatus.idle);
    expect(tunnel.headline, 'Туннель выключен');
  });

  test(
      'системный туннель уходит в CLI через pkexec и не меняется во время работы',
      () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));

    tunnel.setTun(true);
    expect(tunnel.tun, isTrue);

    runner.enqueue();
    await tunnel.start();

    final invocation = runner.invocations.single;
    expect(invocation.executable, 'pkexec');
    expect(invocation.arguments.first, '/opt/warpgen/warpgen');
    expect(invocation.arguments, contains('run'));
    expect(invocation.arguments, contains('--tun'));

    tunnel.setTun(false);
    expect(tunnel.tun, isTrue, reason: 'настройки не меняются во время работы');
  });

  test('отказ pkexec объясняет, что не хватает прав root', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    tunnel.setTun(true);
    final process = runner.enqueue();

    await tunnel.start();
    await process.finish(126); // pkexec: пользователь отменил диалог
    await flushEvents();

    expect(tunnel.status, TunnelStatus.failed);
    expect(tunnel.failure, contains('root'));
    expect(tunnel.failure, contains('диалог'));
  });

  test('ошибку pkexec видно в карточке, а не только в журнале', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    tunnel.setTun(true);
    final process = runner.enqueue();

    await tunnel.start();
    process.emitDiagnostic(
      'Error accessing /tmp/.mount_x/usr/bin/warpgen: Permission denied',
    );
    await process.finish(127);
    await flushEvents();

    expect(tunnel.status, TunnelStatus.failed);
    expect(tunnel.failure, contains('Permission denied'));
  });

  test('отсутствие агента polkit объясняется подсказкой', () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    tunnel.setTun(true);
    final process = runner.enqueue();

    await tunnel.start();
    process.emitDiagnostic('Error executing command as another user: '
        'No authentication agent found.');
    await process.finish(127);
    await flushEvents();

    expect(tunnel.status, TunnelStatus.failed);
    expect(tunnel.failure, contains('polkit'));
  });

  test('события системного туннеля доводят состояние до «подключено»',
      () async {
    final runner = FakeProcessRunner();
    final tunnel = TunnelController(cli: fakeCli(runner: runner));
    tunnel.setTun(true);
    final process = runner.enqueue();

    await tunnel.start();
    process
      ..emitEvent(const {
        'event': 'ready',
        'socks': '127.0.0.1:8086',
        'ip': '203.0.113.9',
      })
      ..emitEvent(const {'event': 'tun_up', 'tun': 'warpgen0', 'mtu': 1280})
      ..emitEvent(const {
        'event': 'routes_applied',
        'tun': 'warpgen0',
        'routes': 4,
      })
      ..emitEvent(const {'event': 'dns_selected', 'dns': '1.1.1.1'});
    await flushEvents();

    expect(tunnel.status, TunnelStatus.live);
    expect(tunnel.detail, contains('весь трафик машины'));
    expect(tunnel.dns, '1.1.1.1');
    expect(
      tunnel.journal.any((entry) => entry.text.contains('warpgen0')),
      isTrue,
      reason: 'маршруты и TUN должны быть в журнале',
    );
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
