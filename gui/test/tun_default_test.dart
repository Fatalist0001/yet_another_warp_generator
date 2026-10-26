import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app.dart';

void main() {
  group('initialTunEnabled', () {
    test('Android всегда включает системный туннель', () {
      expect(
        initialTunEnabled(
          isAndroid: true,
          isWindows: false,
          firstRun: false,
          saved: false,
        ),
        isTrue,
      );
    });

    test('Windows на первом запуске включает системный туннель', () {
      expect(
        initialTunEnabled(
          isAndroid: false,
          isWindows: true,
          firstRun: true,
          saved: false,
        ),
        isTrue,
      );
    });

    test('Windows уважает сохранённый выбор', () {
      expect(
        initialTunEnabled(
          isAndroid: false,
          isWindows: true,
          firstRun: false,
          saved: false,
        ),
        isFalse,
      );
      expect(
        initialTunEnabled(
          isAndroid: false,
          isWindows: true,
          firstRun: false,
          saved: true,
        ),
        isTrue,
      );
    });

    test('Linux включает TUN только по сохранённому выбору', () {
      expect(
        initialTunEnabled(
          isAndroid: false,
          isWindows: false,
          firstRun: true,
          saved: false,
        ),
        isFalse,
      );
    });
  });
}
