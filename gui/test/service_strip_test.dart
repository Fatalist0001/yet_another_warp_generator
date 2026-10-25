import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/app_theme.dart';
import 'package:warpgen_gui/src/controllers/services_controller.dart';
import 'package:warpgen_gui/src/models/service_status.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';
import 'package:warpgen_gui/src/services/warpgen_cli.dart';
import 'package:warpgen_gui/src/widgets/service_strip.dart';

import 'support/fakes.dart';

/// CLI без процесса: отдаёт заранее подготовленный отчёт.
class _StubCli extends WarpgenCli {
  _StubCli()
      : super(
          runner: FakeProcessRunner(),
          location: const WarpgenLocation(
            binaryPath: '/opt/warpgen/warpgen',
            accountFile: '/opt/warpgen/warpgen-account.json',
            source: 'тест',
          ),
        );

  @override
  Future<ServiceReport> checkServices(String socks) async {
    return ServiceReport(
      socks: socks,
      services: const [
        ServiceStatus(
          id: 'yandex',
          label: 'Яндекс',
          category: ServiceCategory.ru,
          state: ServiceState.ok,
          code: 200,
          rttMs: 1700,
        ),
        ServiceStatus(
          id: 'vk',
          label: 'VK',
          category: ServiceCategory.ru,
          state: ServiceState.slow,
          code: 200,
          rttMs: 5800,
        ),
        ServiceStatus(
          id: 'chatgpt',
          label: 'ChatGPT',
          category: ServiceCategory.blocked,
          state: ServiceState.httpError,
          code: 403,
          rttMs: 375,
        ),
        ServiceStatus(
          id: 'ozon',
          label: 'Ozon',
          category: ServiceCategory.ru,
          state: ServiceState.networkError,
          rttMs: 7000,
        ),
      ],
    );
  }
}

void main() {
  testWidgets('красные только совсем недоступные сервисы', (tester) async {
    final services = ServicesController(cli: _StubCli());
    await services.check('127.0.0.1:8086');

    await tester.pumpWidget(
      MaterialApp(
        theme: AppTheme.light(),
        home: Scaffold(
          body: ServiceStrip(services: services, enabled: true),
        ),
      ),
    );
    await tester.pump();

    final scheme = AppTheme.light().colorScheme;
    Color? chipColor(String id) {
      final container = tester.widget<Container>(
        find.byKey(ValueKey('service-$id')),
      );
      return (container.decoration! as BoxDecoration).color;
    }

    expect(chipColor('yandex'), scheme.secondaryContainer);
    expect(chipColor('vk'), scheme.secondaryContainer);
    expect(
      chipColor('chatgpt'),
      scheme.tertiaryContainer.withValues(alpha: 0.7),
    );
    expect(
      chipColor('ozon'),
      scheme.errorContainer.withValues(alpha: 0.55),
    );
    expect(find.text('доступно 2 из 4'), findsOneWidget);
    expect(find.byTooltip('HTTP 403: сервер ответил ошибкой'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byType(ServiceStrip),
        matching: find.byType(Icon),
      ),
      findsNothing,
      reason: 'в пунктах интерфейса не должно быть иконок',
    );
  });
}
