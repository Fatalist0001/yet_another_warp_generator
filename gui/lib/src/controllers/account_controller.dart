/// Аккаунт WARP: чтение файла и регистрация через CLI.
library;

import 'package:flutter/foundation.dart';

import '../services/warpgen_cli.dart';

/// Контроллер аккаунта WARP: чтение файла и регистрация.
class AccountController extends ChangeNotifier {
  AccountController({required this.cli});

  final WarpgenCli cli;

  AccountInfo? _account;
  bool _busy = false;
  bool _loaded = false;
  String? _error;
  bool _disposed = false;
  Future<void>? _refreshFuture;
  Future<void>? _registerFuture;

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
  ///
  /// Повторный вызов во время чтения возвращает тот же Future: при старте GUI
  /// уже запускает refresh, и подключение не должно обгонять его.
  Future<void> refresh() {
    final current = _refreshFuture;
    if (current != null) return current;
    final future = _refresh();
    _refreshFuture = future.whenComplete(() => _refreshFuture = null);
    return _refreshFuture!;
  }

  Future<void> _refresh() async {
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

  /// Гарантирует аккаунт: читает файл, при необходимости регистрирует.
  ///
  /// Возвращает текст ошибки или `null`, если аккаунт готов. Вызывается из
  /// TunnelController перед первым запуском — пользователь не должен нажимать
  /// «зарегистрироваться» отдельно.
  Future<String?> ensure() async {
    final pendingRegister = _registerFuture;
    if (pendingRegister != null) await pendingRegister;
    if (!_loaded) {
      await refresh();
    }
    if (_account?.isComplete == true) return null;
    await register();
    if (_account?.isComplete == true) return null;
    return _error ?? 'не удалось зарегистрировать аккаунт WARP';
  }

  /// Регистрирует аккаунт. `fresh` — перерегистрироваться поверх текущего.
  ///
  /// Пока регистрация идёт, повторный вызов ждёт её, а не запускает вторую:
  /// так кнопка «Подключиться» не создаёт два аккаунта одновременно.
  Future<void> register({bool fresh = false}) {
    final current = _registerFuture;
    if (current != null) return current;
    final future = _register(fresh: fresh);
    _registerFuture = future.whenComplete(() => _registerFuture = null);
    return _registerFuture!;
  }

  Future<void> _register({required bool fresh}) async {
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
