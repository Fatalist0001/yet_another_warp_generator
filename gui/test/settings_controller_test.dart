import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/controllers/settings_controller.dart';
import 'package:warpgen_gui/src/models/dns_preset.dart';
import 'package:warpgen_gui/src/models/gui_settings.dart';
import 'package:warpgen_gui/src/services/settings_store.dart';

import 'support/fakes.dart';

void main() {
  late Directory dir;
  late SettingsStore store;

  setUp(() {
    dir = Directory.systemTemp.createTempSync('warpgen-gui-settings');
    store = SettingsStore(path: '${dir.path}/gui.json');
  });

  tearDown(() {
    if (dir.existsSync()) dir.deleteSync(recursive: true);
  });

  test('сохраняет и читает настройки из gui.json', () async {
    const settings = GuiSettings(
      warpOnly: false,
      tun: true,
      countries: ['NL', 'DE'],
      bind: '127.0.0.1:9999',
      dnsId: 'custom',
      customDns: '1.1.1.1',
      checkServices: false,
      themeMode: 'dark',
    );
    await store.save(settings);

    final loaded = await store.load();
    expect(loaded.warpOnly, isFalse);
    expect(loaded.tun, isTrue);
    expect(loaded.countries, ['NL', 'DE']);
    expect(loaded.bind, '127.0.0.1:9999');
    expect(loaded.dnsId, 'custom');
    expect(loaded.customDns, '1.1.1.1');
    expect(loaded.checkServices, isFalse);
    expect(loaded.themeMode, 'dark');
  });

  test('битый файл не ломает загрузку', () async {
    await File('${dir.path}/gui.json').writeAsString('{не json');
    final loaded = await store.load();
    expect(loaded.warpOnly, isTrue);
    expect(loaded.dnsId, 'recommended');
  });

  test('рекомендуемый DNS сохраняет порядок Comss → Malw → GeoHide', () async {
    final controller = SettingsController(
      cli: fakeCli(runner: FakeProcessRunner()),
      store: store,
      initialPresets: const [
        DnsPreset(
          id: 'recommended',
          label: 'Рекомендуемые',
          value: 'dns.comss.one,dns.malw.link,dns.geohide.ru',
          kind: DnsKind.group,
        ),
      ],
    );
    addTearDown(controller.dispose);

    expect(
      await controller.resolveDns(),
      ['dns.comss.one', 'dns.malw.link', 'dns.geohide.ru'],
    );
  });

  test('свой DNS разбивается по запятой', () async {
    final controller = SettingsController(
      cli: fakeCli(runner: FakeProcessRunner()),
      store: store,
      initialSettings:
          const GuiSettings(dnsId: 'custom', customDns: '1.1.1.1, 8.8.8.8'),
      initialPresets: const [
        DnsPreset(
          id: 'custom',
          label: 'Свой',
          value: '',
          kind: DnsKind.custom,
        ),
      ],
    );
    addTearDown(controller.dispose);

    expect(await controller.resolveDns(), ['1.1.1.1', '8.8.8.8']);
  });

  test('DNS провайдера берётся из dns-current', () async {
    final runner = FakeProcessRunner();
    final controller = SettingsController(
      cli: fakeCli(runner: runner),
      store: store,
      initialSettings: const GuiSettings(dnsId: 'provider'),
      initialPresets: const [
        DnsPreset(
          id: 'provider',
          label: 'Провайдер',
          value: '',
          kind: DnsKind.provider,
        ),
      ],
    );
    addTearDown(controller.dispose);
    final process = runner.enqueue();

    final future = controller.resolveDns();
    await flushEvents();
    process.emitLine('{"interface":"eth0","servers":["192.168.1.1"]}');
    await flushEvents();
    await process.finish(0);

    expect(await future, ['192.168.1.1']);
    expect(controller.providerDns?.interfaceName, 'eth0');
  });

  test('updateConnection пишет режим, TUN, страны и bind', () async {
    final controller = SettingsController(
      cli: fakeCli(runner: FakeProcessRunner()),
      store: store,
    );
    addTearDown(controller.dispose);

    controller.updateConnection(
      warpOnly: false,
      tun: true,
      countries: const ['NL'],
      bind: '127.0.0.1:7777',
    );

    // Сохранение асинхронное: на медленном диске 20 мс мало, поэтому ждём
    // появления записи, а не фиксированную паузу.
    var loaded = await store.load();
    for (var i = 0; i < 100 && loaded.bind != '127.0.0.1:7777'; i++) {
      await Future<void>.delayed(const Duration(milliseconds: 10));
      loaded = await store.load();
    }
    expect(loaded.warpOnly, isFalse);
    expect(loaded.tun, isTrue);
    expect(loaded.countries, ['NL']);
    expect(loaded.bind, '127.0.0.1:7777');
  });
}
