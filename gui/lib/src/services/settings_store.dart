/// Чтение и атомарная запись `~/.warpgen/gui.json`.
library;

import 'dart:convert';
import 'dart:io';

import '../models/gui_settings.dart';

/// Хранилище настроек GUI.
class SettingsStore {
  const SettingsStore({required this.path});

  /// Полный путь к файлу настроек.
  final String path;

  /// Читает настройки. Битый или отсутствующий файл — значения по умолчанию:
  /// интерфейс не должен падать из-за настроек.
  Future<GuiSettings> load() async {
    final file = File(path);
    if (!await file.exists()) {
      return const GuiSettings();
    }
    try {
      final data = jsonDecode(await file.readAsString());
      if (data is Map) {
        return GuiSettings.fromJson(data.cast<String, Object?>());
      }
    } on FormatException {
      // Битый JSON — как отсутствие файла.
    } on FileSystemException {
      // Нет доступа — значения по умолчанию.
    }
    return const GuiSettings();
  }

  /// Сохраняет настройки атомарно: сначала временный файл, потом rename.
  Future<void> save(GuiSettings settings) async {
    final file = File(path);
    await file.parent.create(recursive: true);
    final tmp = File('$path.tmp');
    await tmp.writeAsString(
      const JsonEncoder.withIndent('  ').convert(settings.toJson()),
      flush: true,
    );
    await tmp.rename(path);
  }
}
