// Живой скан endpoint'ов через настоящий CLI и Cloudflare.
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/models/scan_report.dart';

import 'support/live_env.dart';

void main() {
  test('scan: прогресс, отчёт и ранжирование настоящих endpoint\'ов', () async {
    final env = await liveEnv();
    final cli = env.cli();

    final progress = <int>[];
    final diagnostics = <String>[];
    final report = await cli.scan(
      maxCandidates: 24,
      target: 2,
      onProgress: (done, total) => progress.add(done),
      onDiagnostic: diagnostics.add,
    );

    // Пул кандидатов — тысячи пар IP×порт, но проверяем не больше --max.
    expect(report.candidates, greaterThan(1000));
    expect(report.checked, lessThanOrEqualTo(24));
    expect(report.results, isNotEmpty, reason: 'ни один endpoint не проверен');
    expect(progress, isNotEmpty, reason: 'CLI не отдал прогресс в stderr');

    // Сам отчёт согласован: ранжирование — подмножество проверенных, все хорошие.
    for (final hit in report.results) {
      expect(hit.ip, isNotEmpty);
      expect(hit.port, isNonZero);
    }
    expect(report.ranked, isNotEmpty,
        reason: 'скан не нашёл ни одного рабочего endpoint');
    expect(report.ranked.every((hit) => hit.isGood), isTrue);
    expect(report.ranked.every((hit) => hit.status == ScanStatus.ok), isTrue);
    final checkedEndpoints = report.results.map((hit) => hit.endpoint).toSet();
    expect(
      report.ranked.every((hit) => checkedEndpoints.contains(hit.endpoint)),
      isTrue,
      reason: 'в ranked попали endpoint\'ы, которых нет в results',
    );
  }, timeout: const Timeout(Duration(minutes: 4)));
}
