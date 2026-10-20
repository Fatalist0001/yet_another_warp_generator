import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
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
    });

    test('в режиме с Psiphon передаёт страны и cache-dir', () async {
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

  group('scan', () {
    test('собирает аргументы, прогресс и отчёт', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();
      final progress = <String>[];
      final diagnostics = <String>[];

      final future = cli.scan(
        maxCandidates: 32,
        target: 2,
        strictRegion: true,
        onProgress: (done, total) => progress.add('$done/$total'),
        onDiagnostic: diagnostics.add,
      );
      await flushEvents();

      final args = runner.invocations.single.arguments;
      expect(args, containsAllInOrder(['--max', '32']));
      expect(args, containsAllInOrder(['--target', '2']));
      expect(args, contains('--strict-region'));
      expect(args, contains('--debug'));

      process.emitDiagnostic('  проверено 5/32');
      process.emitDiagnostic('предупреждение сети');
      process.emitLine(
        '{"candidates":32,"results":[],"ranked":[{"IP":"162.159.192.1",'
        '"Port":2408,"Loc":"NL","Colo":"AMS","RTT":42000000,"Loss":0,'
        '"Torn":false,"Status":0}]}',
      );
      await flushEvents();
      await process.finish(0);

      final report = await future;
      expect(progress, ['5/32']);
      expect(diagnostics, ['предупреждение сети']);
      expect(report.candidates, 32);
      expect(report.ranked.single.endpoint, '162.159.192.1:2408');
    });

    test('ненулевой код возврата превращается в понятную ошибку', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.scan(maxCandidates: 4, target: 1);
      await flushEvents();
      process.emitDiagnostic('нет аккаунта warpgen');
      await flushEvents();
      await process.finish(1);

      await expectLater(
        future,
        throwsA(
          isA<WarpgenException>().having(
            (error) => error.message,
            'message',
            contains('нет аккаунта warpgen'),
          ),
        ),
      );
    });
  });

  group('generateConfig', () {
    test('передаёт endpoint, путь и DNS', () async {
      final runner = FakeProcessRunner();
      final cli = fakeCli(runner: runner);
      final process = runner.enqueue();

      final future = cli.generateConfig(
        endpoint: '162.159.192.1:2408',
        outputPath: '/home/tester/.warpgen/conf/warp.conf',
      );
      await flushEvents();
      process.emitLine(
        'Конфиг сохранён в /home/tester/.warpgen/conf/warp.conf',
      );
      await process.finish(0);

      final path = await future;
      expect(path, '/home/tester/.warpgen/conf/warp.conf');

      final args = runner.invocations.single.arguments;
      expect(args.first, 'gen');
      expect(args, containsAllInOrder(['--endpoint', '162.159.192.1:2408']));
      expect(args, containsAllInOrder(['--dns', '1.1.1.1']));
      expect(args, containsAllInOrder(['--output', path]));
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
