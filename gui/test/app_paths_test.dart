import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:warpgen_gui/src/platform/app_paths.dart';

void main() {
  test('homeDirectory берёт HOME, затем USERPROFILE', () {
    expect(homeDirectory(const {'HOME': '/home/tester'}), '/home/tester');
    expect(
      homeDirectory(const {'USERPROFILE': r'C:\Users\tester'}),
      r'C:\Users\tester',
    );
    expect(
      homeDirectory(const {'HOME': ' ', 'USERPROFILE': '/tmp/profile'}),
      '/tmp/profile',
    );
  });

  test('на Linux данные лежат в ~/.warpgen', () async {
    final paths = await resolveAppPaths(
      isWindows: false,
      homeDir: '/home/tester',
      executablePath: '/opt/warpgen/warpgen_gui',
    );

    expect(paths.portable, isFalse);
    expect(
      paths.dataDir,
      '/home/tester${Platform.pathSeparator}.warpgen',
    );
  });

  test('на Windows данные лежат рядом с exe', () async {
    final root = await Directory.systemTemp.createTemp('warpgen-paths');
    addTearDown(() => root.delete(recursive: true));
    final appDir = Directory('${root.path}${Platform.pathSeparator}app')
      ..createSync();

    final paths = await resolveAppPaths(
      isWindows: true,
      homeDir: '${root.path}${Platform.pathSeparator}home',
      executablePath:
          '${appDir.path}${Platform.pathSeparator}warpgen_gui.exe',
    );

    expect(paths.portable, isTrue);
    expect(
      paths.dataDir,
      '${appDir.path}${Platform.pathSeparator}warpgen-data',
    );
    expect(Directory(paths.dataDir).existsSync(), isTrue);
  });

  test('если рядом с exe писать нельзя — откат в домашний каталог', () async {
    final paths = await resolveAppPaths(
      isWindows: true,
      homeDir: r'C:\Users\tester',
      executablePath: r'C:\Program Files\warpgen\warpgen_gui.exe',
      ensureDirectory: (_) async =>
          throw const FileSystemException('нет доступа'),
    );

    expect(paths.portable, isFalse);
    expect(
      paths.dataDir,
      'C:\\Users\\tester${Platform.pathSeparator}.warpgen',
    );
  });
}
