import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/controllers/scan_controller.dart';

import 'support/fakes.dart';

void main() {
  test(
    'скан отдаёт прогресс и отчёт, конфиг пишется в каталог приложения',
    () async {
      final runner = FakeProcessRunner();
      final dir = await Directory.systemTemp.createTemp('warpgen-gui-scan');
      addTearDown(() => dir.delete(recursive: true));
      final controller = ScanController(
        cli: fakeCli(runner: runner),
        outputDir: '${dir.path}/conf',
      );

      final process = runner.enqueue();
      final future = controller.scan(maxCandidates: 8, target: 2);
      await flushEvents();

      expect(controller.running, isTrue);

      process.emitDiagnostic('  проверено 3/8');
      await flushEvents();
      expect(controller.done, 3);
      expect(controller.total, 8);
      expect(controller.progress, closeTo(0.375, 0.0001));

      process.emitLine(
        '{"candidates":8,"results":[],"ranked":[{"IP":"162.159.192.1",'
        '"Port":2408,"Loc":"NL","Colo":"AMS","RTT":42000000,"Loss":0,'
        '"Torn":false,"Status":0}]}',
      );
      await flushEvents();
      await process.finish(0);
      await future;

      expect(controller.running, isFalse);
      expect(controller.error, isNull);
      expect(controller.report, isNotNull);
      final hit = controller.report!.ranked.single;
      expect(hit.endpoint, '162.159.192.1:2408');

      final genProcess = runner.enqueue();
      final genFuture = controller.generateConfig(hit);
      await flushEvents();
      genProcess.emitLine('Конфиг сохранён');
      await genProcess.finish(0);
      final path = await genFuture;

      expect(path, '${dir.path}/conf/warp-162.159.192.1_2408.conf');
      expect(await Directory('${dir.path}/conf').exists(), isTrue);
      expect(controller.lastConfigPath, path);
      expect(
        runner.invocations.last.arguments,
        containsAllInOrder(['--output', path]),
      );
    },
  );

  test('ошибка скана сохраняется и снимает флаг работы', () async {
    final runner = FakeProcessRunner();
    final controller = ScanController(
      cli: fakeCli(runner: runner),
      outputDir: '/tmp/warpgen-gui-conf',
    );

    final process = runner.enqueue();
    final future = controller.scan();
    await flushEvents();
    process.emitDiagnostic('нет аккаунта warpgen');
    await flushEvents();
    await process.finish(2);
    await future;

    expect(controller.running, isFalse);
    expect(controller.report, isNull);
    expect(controller.error, contains('нет аккаунта warpgen'));
  });

  test('повторный вызов во время скана игнорируется', () async {
    final runner = FakeProcessRunner();
    final controller = ScanController(
      cli: fakeCli(runner: runner),
      outputDir: '/tmp/warpgen-gui-conf',
    );

    final process = runner.enqueue();
    final future = controller.scan();
    await flushEvents();

    await controller.scan();
    expect(runner.invocations, hasLength(1));

    process.emitLine('{"candidates":0,"results":[],"ranked":[]}');
    await flushEvents();
    await process.finish(0);
    await future;
  });
}
