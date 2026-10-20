/// Аккаунт WARP: чтение файла и регистрация через CLI.
library;

import 'package:flutter/foundation.dart';

import '../services/warpgen_cli.dart';

/// Контроллер страницы аккаунта.
class AccountController extends ChangeNotifier {
  AccountController({required this.cli});

  final WarpgenCli cli;

  AccountInfo? _account;
  bool _busy = false;
  bool _loaded = false;
  String? _error;
  bool _disposed = false;

  /// Текущий аккаунт, `null` — ещё не зарегистрирован.
  AccountInfo? get account => _account;

  /// Идёт ли регистрация.
  bool get busy => _busy;

  /// Читали ли файл аккаунта (чтобы не показывать «пусто» до загрузки).
  bool get loaded => _loaded;

  /// Ошибка последней операции.
  String? get error => _error;

  /// Путь к файлу аккаунта — его видит пользователь в интерфейсе.
  String get accountFile => cli.accountFile;

  /// Перечитывает аккаунт с диска.
  Future<void> refresh() async {
    _busy = true;
    _error = null;
    notifyListeners();
    try {
      _account = await cli.readAccount();
    } catch (error) {
      _error = '$error';
    } finally {
      _busy = false;
      _loaded = true;
      notifyListeners();
    }
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  @override
  void notifyListeners() {
    // Чтение файла и регистрация идут в сети: к закрытому окну они опаздывают.
    if (_disposed) return;
    super.notifyListeners();
  }

  /// Регистрирует аккаунт. `fresh` — перерегистрироваться поверх текущего.
  Future<void> register({bool fresh = false}) async {
    if (_busy) return;
    _busy = true;
    _error = null;
    notifyListeners();
    try {
      _account = await cli.register(fresh: fresh);
      _loaded = true;
    } on WarpgenException catch (error) {
      _error = error.message;
    } catch (error) {
      _error = '$error';
    } finally {
      _busy = false;
      notifyListeners();
    }
  }
}
