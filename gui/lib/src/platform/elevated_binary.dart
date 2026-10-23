/// Копия бинарников, которую можно запустить от root через `pkexec`.
///
/// `pkexec` проверяет доступ к программе **до** запроса пароля. AppImage
/// монтирует содержимое через FUSE с `user_id=<uid>` и без `allow_other`:
/// root не может прочитать файл, и `pkexec` падает с
/// «Error accessing …: Permission denied», не показывая диалог. Поэтому для
/// режима `--tun` бинарник (и `warp-plus`, если он лежит рядом) копируется в
/// домашний каталог пользователя — он доступен root.
library;

import 'dart:io';

/// Готовит копию бинарника для запуска от root.
class ElevatedBinary {
  const ElevatedBinary({required this.sourcePath, required this.targetDir});

  /// Откуда копировать (бинарник, который нашёл GUI).
  final String sourcePath;

  /// Куда копировать: каталог, доступный root.
  final String targetDir;

  /// Синхронизирует копии и возвращает путь к `warpgen` в [targetDir].
  ///
  /// Копирование идёт через временный файл и `rename`, поэтому оборванный
  /// запуск не оставит наполовину записанный бинарник.
  Future<String> prepare() async {
    final source = File(sourcePath);
    if (!await source.exists()) {
      throw FileSystemException('бинарник не найден', sourcePath);
    }
    final dir = Directory(targetDir);
    if (!await dir.exists()) {
      await dir.create(recursive: true);
    }

    final staged = await _sync(source);
    // warp-plus CLI ищет рядом с собой: копируем и его, иначе режим
    // WARP + Psiphon под root не найдёт бинарник.
    final warpPlus = File(
      '${source.parent.path}${Platform.pathSeparator}warp-plus',
    );
    if (await warpPlus.exists()) {
      await _sync(warpPlus);
    }
    return staged;
  }

  Future<String> _sync(File source) async {
    final name = source.uri.pathSegments.last;
    final target = File('$targetDir${Platform.pathSeparator}$name');
    final sourceStat = await source.stat();

    if (await target.exists()) {
      final targetStat = await target.stat();
      if (targetStat.size == sourceStat.size &&
          targetStat.modified == sourceStat.modified &&
          targetStat.mode == sourceStat.mode) {
        return target.path;
      }
    }

    final temp = File('${target.path}.new');
    // File.copy в dart:io сохраняет права POSIX, включая бит исполнения.
    await source.copy(temp.path);
    await temp.rename(target.path);
    await target.setLastModified(sourceStat.modified);
    return target.path;
  }
}
