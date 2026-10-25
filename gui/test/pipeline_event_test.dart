import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/models/pipeline_event.dart';

void main() {
  group('PipelineEvent.fromJson', () {
    test('разбирает ready с наносекундными долями секунды', () {
      final event = parsePipelineLine(
        '{"event":"ready","time":"2020-05-06T07:08:09.123456789+00:00",'
        '"socks":"127.0.0.1:8086","country":"NL","ip":"203.0.113.9",'
        '"org":"AS1234 Example","seconds":42.4}',
      );

      expect(event, isNotNull);
      expect(event!.kind, 'ready');
      expect(event.time.toUtc().year, 2020);
      expect(event.time.millisecond, 123);
      expect(event.socks, '127.0.0.1:8086');
      expect(event.country, 'NL');
      expect(event.ip, '203.0.113.9');
      expect(event.org, 'AS1234 Example');
      expect(event.seconds, closeTo(42.4, 0.001));
      expect(event.severity, EventSeverity.success);
      expect(event.description, contains('socks5://127.0.0.1:8086'));
      expect(event.description, contains('страна NL'));
      expect(event.description, contains('IP 203.0.113.9'));
    });

    test('описывает выбранный endpoint', () {
      final event = PipelineEvent.fromJson(const {
        'event': 'endpoint_selected',
        'endpoint': '162.159.192.1:2408',
        'colo': 'AMS',
        'region': 'NL',
        'rtt_ms': 42,
      });

      expect(event.description, contains('162.159.192.1:2408'));
      expect(event.description, contains('узел AMS'));
      expect(event.description, contains('42 мс'));
      expect(event.severity, EventSeverity.info);
    });

    test('разбирает замер скорости', () {
      final testing = PipelineEvent.fromJson(const {
        'event': 'speed_testing',
        'endpoint': '162.159.192.1:2408',
      });
      expect(testing.description, contains('измеряем скорость'));
      expect(testing.description, contains('162.159.192.1:2408'));

      final checked = PipelineEvent.fromJson(const {
        'event': 'speed_checked',
        'endpoint': '162.159.192.1:2408',
        'mbps': 42.5,
        'bytes': 524288,
      });
      expect(checked.mbps, closeTo(42.5, 0.001));
      expect(checked.bytes, 524288);
      expect(checked.description, contains('42.5 Мбит/с'));

      final selected = PipelineEvent.fromJson(const {
        'event': 'endpoint_selected',
        'endpoint': '162.159.192.1:2408',
        'mbps': 42.5,
      });
      expect(selected.description, contains('42.5 Мбит/с'));
    });

    test('просадка — предупреждение, остановка с ошибкой — ошибка', () {
      final degraded = PipelineEvent.fromJson(const {
        'event': 'degraded',
        'reason': 'нет trace',
        'fails': 2,
      });
      expect(degraded.severity, EventSeverity.warning);
      expect(degraded.description, contains('сбой 2 подряд'));
      expect(degraded.description, contains('нет trace'));

      final stopped = PipelineEvent.fromJson(const {
        'event': 'stopped',
        'error': 'не удалось установить хендшейк',
      });
      expect(stopped.severity, EventSeverity.error);
      expect(stopped.isStopped, isTrue);
      expect(stopped.description, contains('не удалось установить хендшейк'));
    });

    test('неизвестное событие не ломает разбор', () {
      final event = PipelineEvent.fromJson(const {'event': 'something_new'});
      expect(event.kind, 'something_new');
      expect(event.description, 'something_new');
    });

    test('пустые поля не попадают в описание как null', () {
      final event = PipelineEvent.fromJson(const {
        'event': 'endpoint_selected',
      });
      expect(event.description, contains('?'));
      expect(event.description, isNot(contains('null')));
    });
  });

  group('parsePipelineLine', () {
    test('обычные строки и мусор возвращают null', () {
      expect(parsePipelineLine(''), isNull);
      expect(parsePipelineLine('   '), isNull);
      expect(parsePipelineLine('готово: socks5://127.0.0.1:8086'), isNull);
      expect(parsePipelineLine('{не json}'), isNull);
      expect(parsePipelineLine('[1,2,3]'), isNull);
    });

    test('JSON-объект без события не падает', () {
      final event = parsePipelineLine('{"foo":1}');
      expect(event, isNotNull);
      expect(event!.kind, '');
    });
  });
}
