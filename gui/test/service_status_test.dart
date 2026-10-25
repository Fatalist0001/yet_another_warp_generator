import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/models/service_status.dart';

void main() {
  ServiceStatus status(ServiceState state) => ServiceStatus(
        id: 'x',
        label: 'X',
        category: ServiceCategory.blocked,
        state: state,
      );

  group('ServiceStatus', () {
    test('ok и slow считаются доступными', () {
      for (final state in [ServiceState.ok, ServiceState.slow]) {
        final item = status(state);
        expect(item.isOk, isTrue);
        expect(item.isReachable, isTrue);
        expect(item.isUnavailable, isFalse);
        expect(item.isHttpError, isFalse);
      }
    });

    test('HTTP-ошибка — сервер ответил, но это предупреждение', () {
      final item = status(ServiceState.httpError);
      expect(item.isOk, isFalse);
      expect(item.isReachable, isTrue);
      expect(item.isHttpError, isTrue);
      expect(item.isUnavailable, isFalse);
    });

    test('таймаут и сетевая ошибка — совсем недоступен', () {
      for (final state in [
        ServiceState.timeout,
        ServiceState.networkError,
      ]) {
        final item = status(state);
        expect(item.isOk, isFalse);
        expect(item.isReachable, isFalse);
        expect(item.isUnavailable, isTrue);
      }
    });
  });
}
