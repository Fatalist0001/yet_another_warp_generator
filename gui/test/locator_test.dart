import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/platform/warpgen_locator.dart';

void main() {
  test('явный путь имеет приоритет и аккаунт лежит рядом', () async {
    final locator = WarpgenLocator(
      environment: const {'PATH': '', 'HOME': '/home/tester'},
      workingDirectory: Directory('/work'),
      binaryName: 'warpgen',
      pathListSeparator: ':',
      pathSeparator: '/',
      fileExists: (path) => path == '/opt/warpgen/warpgen',
    );

    final location = await locator.locate(explicitPath: '/opt/warpgen/warpgen');

    expect(location, isNotNull);
    expect(location!.binaryPath, '/opt/warpgen/warpgen');
    expect(location.accountFile, '/opt/warpgen/warpgen-account.json');
    expect(location.source, 'указано вручную');
  });

  test('WARPGEN_BIN и PATH используются по порядку', () async {
    final locator = WarpgenLocator(
      environment: const {
        'PATH': '/usr/bin:/usr/local/bin',
        'WARPGEN_BIN': '/env/warpgen',
      },
      workingDirectory: Directory('/work'),
      binaryName: 'warpgen',
      pathListSeparator: ':',
      pathSeparator: '/',
      fileExists: (path) =>
          path == '/env/warpgen' || path == '/usr/local/bin/warpgen',
    );

    final location = await locator.locate();
    expect(location!.binaryPath, '/env/warpgen');
    expect(location.source, 'WARPGEN_BIN');

    final pathOnly = WarpgenLocator(
      environment: const {'PATH': '/usr/bin:/usr/local/bin'},
      workingDirectory: Directory('/work'),
      binaryName: 'warpgen',
      pathListSeparator: ':',
      pathSeparator: '/',
      fileExists: (path) => path == '/usr/local/bin/warpgen',
    );
    final found = await pathOnly.locate();
    expect(found!.binaryPath, '/usr/local/bin/warpgen');
    expect(found.source, 'PATH');
  });

  test('WARPGEN_ACCOUNT переопределяет файл аккаунта', () async {
    final locator = WarpgenLocator(
      environment: const {
        'PATH': '',
        'WARPGEN_ACCOUNT': '/home/tester/.warpgen/me.json',
      },
      workingDirectory: Directory('/work'),
      binaryName: 'warpgen',
      pathListSeparator: ':',
      pathSeparator: '/',
      fileExists: (path) => path == '/opt/warpgen/warpgen',
    );

    final location = await locator.locate(explicitPath: '/opt/warpgen/warpgen');
    expect(location!.accountFile, '/home/tester/.warpgen/me.json');
  });

  test('ничего не нашли — null', () async {
    final locator = WarpgenLocator(
      environment: const {'PATH': '/usr/bin'},
      workingDirectory: Directory('/work'),
      binaryName: 'warpgen',
      pathListSeparator: ':',
      pathSeparator: '/',
      fileExists: (_) => false,
    );
    expect(await locator.locate(), isNull);
  });

  test('на Windows ищет warpgen.exe и делит PATH по «;»', () async {
    final locator = WarpgenLocator(
      environment: const {'PATH': r'C:\Tools;C:\Apps'},
      workingDirectory: Directory(r'C:\work'),
      applicationDirectory: Directory(r'C:\app'),
      binaryName: 'warpgen.exe',
      pathListSeparator: ';',
      pathSeparator: r'\',
      fileExists: (path) => path == r'C:\Apps\warpgen.exe',
    );

    final location = await locator.locate();

    expect(location!.binaryPath, r'C:\Apps\warpgen.exe');
    expect(location.source, 'PATH');
    expect(location.accountFile, contains('warpgen-account.json'));
  });

  test('находит CLI рядом с приложением (Windows-сборка)', () async {
    final locator = WarpgenLocator(
      environment: const {'PATH': ''},
      workingDirectory: Directory(r'C:\work'),
      applicationDirectory: Directory(r'C:\Program Files\warpgen'),
      binaryName: 'warpgen.exe',
      pathListSeparator: ';',
      pathSeparator: r'\',
      fileExists: (path) =>
          path == r'C:\Program Files\warpgen\warpgen.exe',
    );

    final location = await locator.locate();

    expect(location!.binaryPath,
        r'C:\Program Files\warpgen\warpgen.exe');
    expect(location.source, 'рядом с приложением');
  });

  test(
    'находит бинарник в текущем каталоге на реальной файловой системе',
    () async {
      final dir = await Directory.systemTemp.createTemp('warpgen-gui-locator');
      addTearDown(() => dir.delete(recursive: true));
      final name = Platform.isWindows ? 'warpgen.exe' : 'warpgen';
      final binary = File('${dir.path}${Platform.pathSeparator}$name');
      await binary.writeAsString('#!/bin/sh\n');

      final locator = WarpgenLocator(
        environment: const {'PATH': ''},
        workingDirectory: dir,
      );
      final location = await locator.locate();

      expect(location, isNotNull);
      expect(location!.binaryPath, binary.path);
      expect(location.source, 'текущий каталог');
      expect(
        location.accountFile,
        '${dir.path}${Platform.pathSeparator}warpgen-account.json',
      );
    },
  );
}
