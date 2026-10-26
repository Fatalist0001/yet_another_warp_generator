/// Каталоги данных GUI.
///
/// Linux и Android держат данные в `~/.warpgen` (AppImage смонтирован только
/// для чтения). Windows-сборка портативная: настройки, аккаунт, кэш и журнал
/// маршрутов лежат в `warpgen-data` рядом с `warpgen_gui.exe` — папку можно
/// распаковать куда угодно и забрать с собой.
library;

import 'dart:io';

/// Результат выбора каталога данных.
class AppPaths {
  const AppPaths({required this.dataDir, required this.portable});

  /// Каталог, где лежат настройки, аккаунт, кэш и журнал маршрутов.
  final String dataDir;

  /// `true` — данные рядом с exe (Windows), `false` — в домашнем каталоге.
  final bool portable;
}

/// Домашний каталог пользователя: `HOME` на Linux, `USERPROFILE` на Windows.
String homeDirectory(Map<String, String> environment) {
  for (final key in const ['HOME', 'USERPROFILE']) {
    final value = environment[key];
    if (value != null && value.trim().isNotEmpty) return value.trim();
  }
  return Directory.current.path;
}

/// Выбирает каталог данных.
///
/// На Windows это `warpgen-data` рядом с приложением; если туда писать нельзя
/// (например, программа лежит в Program Files), возвращаем `~/.warpgen`, чтобы
/// приложение не падало на первом запуске.
Future<AppPaths> resolveAppPaths({
  required bool isWindows,
  required String homeDir,
  required String executablePath,
  Future<void> Function(String path)? ensureDirectory,
}) async {
  final homeData = _join(homeDir, '.warpgen');
  if (!isWindows) {
    return AppPaths(dataDir: homeData, portable: false);
  }
  final portable = _join(File(executablePath).parent.path, 'warpgen-data');
  try {
    if (ensureDirectory != null) {
      await ensureDirectory(portable);
    } else {
      await Directory(portable).create(recursive: true);
    }
    return AppPaths(dataDir: portable, portable: true);
  } on FileSystemException {
    return AppPaths(dataDir: homeData, portable: false);
  }
}

String _join(String dir, String name) => '$dir${Platform.pathSeparator}$name';
