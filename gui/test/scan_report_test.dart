import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/models/scan_report.dart';

/// Ровно тот JSON, что печатает `warpgen scan --json`.
const _reportJson = '{"candidates":32,"ranked":['
    '{"IP":"162.159.192.1","Port":2408,"Loc":"NL","Colo":"AMS","RTT":42000000,"Loss":0.1,"Torn":false,"Status":0},'
    '{"IP":"188.114.96.5","Port":500,"Loc":"","Colo":"","RTT":0,"Loss":0,"Torn":false,"Status":1}'
    '],"results":['
    '{"IP":"162.159.192.1","Port":2408,"Loc":"NL","Colo":"AMS","RTT":42000000,"Loss":0.1,"Torn":false,"Status":0}'
    ']}';

ScanReport _parse(String source) {
  final decoded = jsonDecode(source);
  expect(decoded, isA<Map<String, Object?>>());
  return ScanReport.fromJson(decoded as Map<String, Object?>);
}

void main() {
  test('разбирает отчёт и задержку в наносекундах', () {
    final report = _parse(_reportJson);

    expect(report.candidates, 32);
    expect(report.checked, 1);
    expect(report.ranked, hasLength(2));

    final first = report.ranked.first;
    expect(first.endpoint, '162.159.192.1:2408');
    expect(first.loc, 'NL');
    expect(first.colo, 'AMS');
    expect(first.rtt, const Duration(milliseconds: 42));
    expect(first.rttLabel, '42 мс');
    expect(first.lossLabel, '10%');
    expect(first.statusLabel, 'OK');
    expect(first.isGood, isTrue);

    final second = report.ranked.last;
    expect(second.endpoint, '188.114.96.5:500');
    expect(second.rttLabel, '—');
    expect(second.locLabel, '?');
    expect(second.statusLabel, 'нет хендшейка');
    expect(second.isGood, isFalse);
  });

  test('неизвестный статус не ломает разбор', () {
    final hit = ScanHit.fromJson(const {
      'IP': '1.2.3.4',
      'Port': 1,
      'Status': 99,
    });
    expect(hit.status, ScanStatus.unknown);
    expect(hit.statusLabel, 'неизвестно');
  });

  test('пустой отчёт', () {
    final report = _parse('{"candidates":0,"results":[],"ranked":[]}');
    expect(report.candidates, 0);
    expect(report.checked, 0);
    expect(report.ranked, isEmpty);
  });
}
