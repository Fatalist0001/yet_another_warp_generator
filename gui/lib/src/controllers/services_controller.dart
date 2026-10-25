/// Проверка доступности сервисов через SOCKS5 после подключения.
library;

import 'package:flutter/foundation.dart';

import '../models/service_status.dart';
import '../services/warpgen_cli.dart';

/// Контроллер проверки сервисов.
class ServicesController extends ChangeNotifier {
  ServicesController({required this.cli});

  final WarpgenCli cli;

  bool _checking = false;
  List<ServiceStatus> _results = const [];
  String? _error;
  String? _socks;
  bool _disposed = false;
  int _generation = 0;

  /// Идёт проверка.
  bool get checking => _checking;

  /// Последние результаты в порядке списка CLI.
  List<ServiceStatus> get results => _results;

  /// Ошибка последней проверки.
  String? get error => _error;

  /// Адрес, для которого получены результаты.
  String? get socks => _socks;

  /// Запускает проверку. Повторный вызов во время проверки игнорируется.
  Future<void> check(String socks) async {
    if (_checking) return;
    final generation = ++_generation;
    _checking = true;
    _error = null;
    _socks = socks;
    _notify();
    try {
      final report = await cli.checkServices(socks);
      if (generation != _generation) return;
      _results = report.services;
    } catch (error) {
      if (generation != _generation) return;
      _error = '$error';
      _results = const [];
    } finally {
      if (generation == _generation) {
        _checking = false;
        _notify();
      }
    }
  }

  /// Сбрасывает результаты при отключении. Если проверка ещё шла, её ответ
  /// будет проигнорирован: он относится к уже снятому туннелю.
  void reset() {
    _generation++;
    final changed = _checking || _results.isNotEmpty || _error != null;
    _checking = false;
    _results = const [];
    _error = null;
    _socks = null;
    if (changed) _notify();
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }
}
