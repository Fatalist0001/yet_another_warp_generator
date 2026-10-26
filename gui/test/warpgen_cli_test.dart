import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/models/dns_preset.dart';
import 'package:warpgen_gui/src/models/service_status.dart';
import 'package:warpgen_gui/src/platform/process_runner.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';
import 'package:warpgen_gui/src/services/warpgen_cli.dart';

import 'support/fakes.dart';

void main() {
  group('startRun', () {
    test('в режиме WARP не передаёт страны и cache-dir', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        cacheDir: '/home/tester/.warpgen/chain',
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
      );

      final args = runner.invocations.single.arguments;
      expect(args.first, 'run');
      expect(args, contains('--json'));
      expect(args, contains('--warp-only'));
      expect(args, contains('--account'));
      expect(args, contains('/opt/warpgen/warpgen-account.json'));
      expect(args, containsAllInOrder(['--bind', '127.0.0.1:8086']));
      expect(args, isNot(contains('--countries')));
      expect(args, isNot(contains('--cache-dir')));
      expect(args, isNot(contains('--dns')));
    });

    test('передаёт выбранные DNS в --dns', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
        dns: const ['dns.comss.one', 'dns.malw.link'],
      );

      expect(
        runner.invocations.single.arguments,
        containsAllInOrder(['--dns', 'dns.comss.one,dns.malw.link']),
      );
    });

    test('системный туннель идёт через pkexec с --tun и --cache-dir',
        skip: Platform.isWindows ? 'pkexec есть только на Linux' : false,
        () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        cacheDir: '/home/tester/.warpgen/chain',
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
        tun: true,
      );

      final invocation = runner.invocations.single;
      expect(invocation.executable, 'pkexec');
      expect(invocation.arguments, [
        '/opt/warpgen/warpgen',
        'run',
        '--json',
        '--account',
        '/opt/warpgen/warpgen-account.json',
        '--bind',
        '127.0.0.1:8086',
        '--tun',
        '--warp-only',
        '--cache-dir',
        '/home/tester/.warpgen/chain',
      ]);
    });

    test('для root берёт копию бинарника, а не файл AppImage',
        skip: Platform.isWindows ? 'pkexec есть только на Linux' : false,
        () async {
      final temp = Directory.systemTemp.createTempSync('warpgen-elev-test');
      addTearDown(() => temp.deleteSync(recursive: true));
      final source = Directory('${temp.path}/appimage')..createSync();
      final binary = File('${source.path}/warpgen')
        ..writeAsStringSync('#!/bin/sh\n');
      File('${source.path}/warp-plus').writeAsStringSync('#!/bin/sh\n# plus\n');
      final elev = '${temp.path}/elev';

      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        binaryPath: binary.path,
        accountFile: '${temp.path}/account.json',
        cacheDir: '/home/tester/.warpgen/chain',
        elevDir: elev,
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
        tun: true,
      );

      final invocation = runner.invocations.single;
      expect(invocation.executable, 'pkexec');
      expect(
        invocation.arguments.first,
        '$elev${Platform.pathSeparator}warpgen',
      );
      expect(invocation.arguments, isNot(contains(binary.path)),
          reason:
              'AppImage доступен только пользователю, root его не прочитает');
      expect(File('$elev/warpgen').readAsStringSync(), '#!/bin/sh\n');
      expect(File('$elev/warp-plus').existsSync(), isTrue,
          reason: 'warp-plus ищется рядом с warpgen');
    });

    test('root-процесс останавливается файлом-сигналом, а не SIGINT',
        skip: Platform.isWindows ? 'pkexec есть только на Linux' : false,
        () async {
      final temp = Directory.systemTemp.createTempSync('warpgen-stop-test');
      addTearDown(() => temp.deleteSync(recursive: true));
      final stopFile = '${temp.path}/run/tun.stop';
      Directory('${temp.path}/run').createSync(recursive: true);
      File(stopFile).writeAsStringSync('прошлый запуск');

      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        cacheDir: '/home/tester/.warpgen/chain',
        stopFile: stopFile,
      );
      final inner = runner.enqueue();

      final process = await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
        tun: true,
      );

      final args = runner.invocations.single.arguments;
      expect(args, containsAllInOrder(['--stop-file', stopFile]));
      expect(
        File(stopFile).existsSync(),
        isFalse,
        reason: 'старый файл удалён до запуска',
      );

      process.requestStop();
      expect(File(stopFile).readAsStringSync(), 'stop');
      expect(
        inner.interrupted,
        isTrue,
        reason: 'сигнал тоже отправляется, но для root он не сработает',
      );
    });

    test('на Windows останавливает run файлом-сигналом, а не сигналом', () async {
      final temp = Directory.systemTemp.createTempSync('warpgen-win-stop-test');
      addTearDown(() => temp.deleteSync(recursive: true));
      final stopFile = '${temp.path}/run/stop';

      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        stopFile: stopFile,
        softStopByFile: true,
      );
      final inner = runner.enqueue();

      final process = await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
      );

      final invocation = runner.invocations.single;
      expect(invocation.executable, cli.binaryPath);
      expect(invocation.arguments,
          containsAllInOrder(['--stop-file', stopFile]));
      expect(invocation.arguments, isNot(contains('--tun')));

      process.requestStop();
      expect(File(stopFile).readAsStringSync(), 'stop');
      expect(
        inner.interrupted,
        isFalse,
        reason: 'сигнал на Windows убивает процесс без остановки туннеля',
      );
    });

    test('TUN на Windows запускает CLI напрямую, без pkexec',
        skip: Platform.isWindows ? false : 'проверка Windows-ветки', () async {
      final temp = Directory.systemTemp.createTempSync('warpgen-win-tun-test');
      addTearDown(() => temp.deleteSync(recursive: true));
      final stopFile = '${temp.path}/run/stop';
      final stateDir = '${temp.path}/state';

      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        stopFile: stopFile,
        stateDir: stateDir,
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
        tun: true,
      );

      final invocation = runner.invocations.single;
      expect(invocation.executable, cli.binaryPath);
      expect(invocation.arguments, contains('--tun'));
      expect(invocation.arguments,
          containsAllInOrder(['--stop-file', stopFile]));
      expect(invocation.arguments,
          containsAllInOrder(['--state-dir', stateDir]));
    });

    test('без pkexec объясняет, что нужен polkit',
        skip: Platform.isWindows ? 'pkexec есть только на Linux' : false,
        () async {
      final cli = WarpgenCli(
        runner: _MissingExecRunner(),
        location: const WarpgenLocation(
          binaryPath: '/opt/warpgen/warpgen',
          accountFile: '/opt/warpgen/warpgen-account.json',
          source: 'тест',
        ),
      );

      await expectLater(
        cli.startRun(
          warpOnly: true,
          countries: const ['NL'],
          bind: '127.0.0.1:8086',
          tun: true,
        ),
        throwsA(
          isA<WarpgenException>().having(
            (error) => error.message,
            'message',
            contains('polkit'),
          ),
        ),
      );
    });

    test('передаёт файл кэша endpoint-ов в --endpoint-cache', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        endpointCache: '/home/tester/.warpgen/endpoints.json',
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: true,
        countries: const ['NL'],
        bind: '127.0.0.1:8086',
      );

      expect(
        runner.invocations.single.arguments,
        containsAllInOrder([
          '--endpoint-cache',
          '/home/tester/.warpgen/endpoints.json',
        ]),
      );
    });

    test('clearEndpointCache удаляет сохранённый профиль', () async {
      final temp = Directory.systemTemp.createTempSync('warpgen-cache-test');
      addTearDown(() => temp.deleteSync(recursive: true));
      final path = '${temp.path}/endpoints.json';
      File(path).writeAsStringSync('{}');
      final cli = fakeCli(
        runner: FakeProcessRunner(),
        endpointCache: path,
      );

      await cli.clearEndpointCache();

      expect(File(path).existsSync(), isFalse);
      // Повторный вызов не падает, если файла уже нет.
      await cli.clearEndpointCache();
    });

    test('с Psiphon --cache-dir передаётся и без системного туннеля', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(
        runner: runner,
        cacheDir: '/home/tester/.warpgen/chain',
      );
      runner.enqueue();

      await cli.startRun(
        warpOnly: false,
        countries: const ['NL', 'SE'],
        bind: '127.0.0.1:9050',
      );

      final args = runner.invocations.single.arguments;
      expect(args, containsAllInOrder(['--countries', 'NL,SE']));
      expect(
        args,
        containsAllInOrder(['--cache-dir', '/home/tester/.warpgen/chain']),
      );
      expect(args, containsAllInOrder(['--bind', '127.0.0.1:9050']));
      expect(args, isNot(contains('--warp-only')));
    });
  });

  group('dns-presets', () {
    test('разбирает список и флаг --resolve', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.dnsPresets(resolve: true);
      await flushEvents();
      process.emitLine(
        '[{"id":"recommended","label":"Рекомендуемые","value":'
        '"dns.comss.one,dns.malw.link","kind":"group","default":true},'
        '{"id":"provider","label":"Провайдер","kind":"provider"}]',
      );
      await flushEvents();
      await process.finish(0);

      final presets = await future;
      expect(presets, hasLength(2));
      expect(presets.first.id, 'recommended');
      expect(presets.first.kind, DnsKind.group);
      expect(presets.first.values, ['dns.comss.one', 'dns.malw.link']);
      expect(presets.last.kind, DnsKind.provider);
      expect(
        runner.invocations.single.arguments,
        containsAllInOrder(['dns-presets', '--json', '--resolve']),
      );
    });
  });

  group('dns-current', () {
    test('разбирает серверы и предупреждение', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.dnsCurrent();
      await flushEvents();
      process.emitLine(
        '{"interface":"eth0","servers":["192.168.1.1","1.1.1.1"],'
        '"warning":"локальный DNS"}',
      );
      await flushEvents();
      await process.finish(0);

      final info = await future;
      expect(info.interfaceName, 'eth0');
      expect(info.servers, ['192.168.1.1', '1.1.1.1']);
      expect(info.warning, 'локальный DNS');
    });
  });

  group('services', () {
    test('разбирает отчёт и статусы', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.checkServices('127.0.0.1:8086');
      await flushEvents();
      process.emitLine(
        '{"socks":"127.0.0.1:8086","services":['
        '{"id":"yandex","label":"Яндекс","category":"ru","status":"ok",'
        '"code":200,"rtt_ms":42},'
        '{"id":"youtube","label":"YouTube","category":"blocked",'
        '"status":"timeout"}]}',
      );
      await flushEvents();
      await process.finish(0);

      final report = await future;
      expect(report.socks, '127.0.0.1:8086');
      expect(report.services, hasLength(2));
      expect(report.services.first.isOk, isTrue);
      expect(report.services.last.state, ServiceState.timeout);
      expect(
        runner.invocations.single.arguments,
        containsAllInOrder(['services', '--json', '--socks', '127.0.0.1:8086']),
      );
    });
  });

  group('register', () {
    test('разбирает многострочный JSON и не требует приватный ключ', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.register(fresh: true);
      await flushEvents();
      process
        ..emitLine('{')
        ..emitLine('  "id": "abcd-1234",')
        ..emitLine('  "token": "secret",')
        ..emitLine('  "private_key": "private",')
        ..emitLine('  "peer_public_key": "peer-key",')
        ..emitLine('  "ipv4": "172.16.0.2",')
        ..emitLine('  "ipv6": "2606:4700:110:8a36::2"')
        ..emitLine('}');
      await flushEvents();
      await process.finish(0);

      final account = await future;
      expect(account.id, 'abcd-1234');
      expect(account.ipv4, '172.16.0.2');
      expect(account.peerPublicKey, 'peer-key');
      expect(account.isComplete, isTrue);
      expect(runner.invocations.single.arguments, contains('--fresh'));
    });
  });

  group('readAccount', () {
    test('читает файл аккаунта с диска', () async {
      final dir = await Directory.systemTemp.createTemp('warpgen-gui-account');
      addTearDown(() => dir.delete(recursive: true));
      final file = File('${dir.path}/warpgen-account.json');
      await file.writeAsString(
        '{"id":"x","private_key":"p","peer_public_key":"peer",'
        '"ipv4":"172.16.0.2","ipv6":"::2"}',
      );

      final cli = fakeCli(runner: FakeProcessRunner(), accountFile: file.path);
      final account = await cli.readAccount();

      expect(account, isNotNull);
      expect(account!.ipv4, '172.16.0.2');
      expect(account.isComplete, isTrue);
    });

    test('нет файла — нет аккаунта', () async {
      final cli = fakeCli(
        runner: FakeProcessRunner(),
        accountFile: '/tmp/warpgen-gui-missing-account.json',
      );
      expect(await cli.readAccount(), isNull);
    });
  });
}

/// Раннер, у которого нет `pkexec` (как на системе без polkit).
class _MissingExecRunner implements ProcessRunner {
  @override
  Future<RunningProcess> start(
    String executable,
    List<String> arguments, {
    String? workingDirectory,
    Map<String, String>? environment,
  }) {
    throw ProcessException(executable, arguments, 'No such file or directory');
  }
}
