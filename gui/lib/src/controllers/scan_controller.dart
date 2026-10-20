/// Скан endpoint'ов из интерфейса и генерация `.conf`.
library;

import 'dart:io';

import 'package:flutter/foundation.dart';

import '../models/scan_report.dart';
import '../services/warpgen_cli.dart';

/// Контроллер страницы скана.
class ScanController extends ChangeNotifier {
  ScanController({required this.cli, required this.outputDir});

  /// Куда складывать сгенерированные конфиги (каталог приложения).
  final String outputDir;

  final WarpgenCli cli;

  bool _running = false;
  int _done = 0;
  int _total = 0;
  ScanReport? _report;
  String? _error;
  final List<String> _diagnostics = [];
  String? _lastConfigPath;
  bool _disposed = false;

  /// Идёт ли скан.
  bool get running => _running;

  /// Сколько кандидатов уже проверено.
  int get done => _done;

  /// Сколько кандидатов в этом прогоне.
  int get total => _total;

  /// Прогресс от 0 до 1, `null` — пока неизвестен.
  double? get progress => _total <= 0 ? null : (_done / _total).clamp(0, 1);

  /// Последний отчёт скана.
  ScanReport? get report => _report;

  /// Ошибка последнего прогона.
  String? get error => _error;

  /// Служебные строки CLI (без прогресса).
  List<String> get diagnostics => List<String>.unmodifiable(_diagnostics);

  /// Путь последнего сгенерированного конфига.
  String? get lastConfigPath => _lastConfigPath;

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  @override
  void notifyListeners() {
    // Прогресс скана может прийти уже после закрытия окна.
    if (_disposed) return;
    super.notifyListeners();
  }

  /// Запускает скан. Повторный вызов во время работы игнорируется.
  Future<void> scan({
    int maxCandidates = 256,
    int target = 8,
    bool strictRegion = false,
  }) async {
    if (_running) return;
    _running = true;
    _error = null;
    _done = 0;
    _total = 0;
    _report = null;
    _diagnostics.clear();
    notifyListeners();

    try {
      _report = await cli.scan(
        maxCandidates: maxCandidates,
        target: target,
        strictRegion: strictRegion,
        onProgress: (done, total) {
          _done = done;
          _total = total;
          notifyListeners();
        },
        onDiagnostic: (line) {
          _diagnostics.add(line);
          if (_diagnostics.length > 20) _diagnostics.removeAt(0);
        },
      );
    } on WarpgenException catch (error) {
      _error = error.message;
    } catch (error) {
      _error = '$error';
    } finally {
      _running = false;
      notifyListeners();
    }
  }

  /// Генерирует `.conf` для найденного endpoint'а.
  ///
  /// Файл кладём в каталог приложения: путь всегда известен и его можно
  /// скопировать, не открывая диалог выбора файла.
  Future<String> generateConfig(ScanHit hit, {String dns = '1.1.1.1'}) async {
    final dir = Directory(outputDir);
    if (!await dir.exists()) {
      await dir.create(recursive: true);
    }
    final path = '$outputDir/warp-${hit.ip}_${hit.port}.conf';
    final generated = await cli.generateConfig(
      endpoint: hit.endpoint,
      outputPath: path,
      dns: dns,
    );
    _lastConfigPath = generated;
    notifyListeners();
    return generated;
  }
}
