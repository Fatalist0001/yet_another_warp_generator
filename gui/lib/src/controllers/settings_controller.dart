/// Настройки GUI: DNS-пресеты, выбранный DNS, тема и проверка сервисов.
library;

import 'dart:async';

import 'package:flutter/foundation.dart';

import '../models/dns_preset.dart';
import '../models/gui_settings.dart';
import '../services/settings_store.dart';
import '../services/warpgen_cli.dart';

/// Контроллер настроек.
class SettingsController extends ChangeNotifier {
  SettingsController({
    required this.cli,
    required this.store,
    GuiSettings initialSettings = const GuiSettings(),
    List<DnsPreset> initialPresets = const [],
  })  : _settings = initialSettings,
        _presets = initialPresets;

  final WarpgenCli cli;
  final SettingsStore store;

  GuiSettings _settings;
  List<DnsPreset> _presets;
  bool _loadingPresets = false;
  String? _error;
  DnsCurrent? _provider;
  String? _providerError;
  bool _disposed = false;
  Future<void> _saveChain = Future<void>.value();

  /// Текущие сохранённые настройки.
  GuiSettings get settings => _settings;

  /// Пресеты DNS из CLI.
  List<DnsPreset> get presets => _presets;

  /// Идёт загрузка/резолв пресетов.
  bool get loadingPresets => _loadingPresets;

  /// Ошибка загрузки пресетов или сохранения.
  String? get error => _error;

  /// DNS провайдера, если пользователь выбрал соответствующий пресет.
  DnsCurrent? get providerDns => _provider;

  /// Ошибка определения DNS провайдера.
  String? get providerError => _providerError;

  /// Выбранный пресет (если список уже загружен).
  DnsPreset? get selectedPreset {
    for (final preset in _presets) {
      if (preset.id == _settings.dnsId) return preset;
    }
    return null;
  }

  /// Читает настройки и список пресетов.
  Future<void> load() async {
    try {
      _settings = await store.load();
    } catch (error) {
      // Настройки не должны мешать запуску: битый файл или нет доступа —
      // работаем со значениями по умолчанию.
      _settings = const GuiSettings();
      _error = 'Не удалось прочитать настройки: $error';
    }
    _notify();
    await refreshPresets();

    // Старый gui.json мог ссылаться на удалённый пресет. Не оставляем
    // пользователя с «Список DNS не загружен» на первом подключении.
    if (_presets.isNotEmpty && selectedPreset == null) {
      final fallback = _presets.firstWhere(
        (preset) => preset.isDefault,
        orElse: () => _presets.first,
      );
      _settings = _settings.copyWith(dnsId: fallback.id);
      _scheduleSave();
      _notify();
    }
    if (selectedPreset?.kind == DnsKind.provider) {
      await refreshProvider();
    }
  }

  /// Загружает пресеты; `resolve` дополнительно резолвит hostname'ы.
  Future<void> refreshPresets({bool resolve = false}) async {
    _loadingPresets = true;
    _error = null;
    _notify();
    try {
      _presets = await cli.dnsPresets(resolve: resolve);
    } catch (error) {
      _error = '$error';
    } finally {
      _loadingPresets = false;
      _notify();
    }
  }

  /// Определяет DNS провайдера через CLI.
  Future<void> refreshProvider() async {
    _providerError = null;
    _notify();
    try {
      _provider = await cli.dnsCurrent();
    } catch (error) {
      _provider = null;
      _providerError = '$error';
    }
    _notify();
  }

  /// Возвращает DNS-адреса для `run --dns` с учётом выбранного пресета.
  ///
  /// Для провайдера и «своего» значения результат вычисляется во время
  /// подключения, а не при сохранении настроек: DNS провайдера может смениться.
  Future<List<String>> resolveDns() async {
    final preset = selectedPreset;
    if (preset == null) {
      throw const WarpgenException(
        'Список DNS не загружен. Откройте настройки и обновите его.',
      );
    }
    switch (preset.kind) {
      case DnsKind.provider:
        final info = await cli.dnsCurrent();
        if (info.servers.isEmpty) {
          throw const WarpgenException('Не удалось определить DNS провайдера');
        }
        _provider = info;
        _providerError = null;
        _notify();
        return info.servers;
      case DnsKind.custom:
        final values = _settings.customDns
            .split(',')
            .map((part) => part.trim())
            .where((part) => part.isNotEmpty)
            .toList(growable: false);
        if (values.isEmpty) {
          throw const WarpgenException('Укажите DNS в настройках');
        }
        return values;
      case DnsKind.group:
      case DnsKind.hostname:
      case DnsKind.ip:
        final values = preset.values;
        if (values.isEmpty) {
          throw const WarpgenException('У выбранного DNS нет адреса');
        }
        return values;
    }
  }

  /// Выбирает DNS-пресет.
  void setDnsId(String id) {
    if (id == _settings.dnsId) return;
    _settings = _settings.copyWith(dnsId: id);
    _notify();
    _scheduleSave();
    if (_isProvider(id)) {
      unawaited(refreshProvider());
    }
  }

  /// Меняет «свой» DNS.
  void setCustomDns(String value) {
    if (value == _settings.customDns) return;
    _settings = _settings.copyWith(customDns: value);
    _notify();
    _scheduleSave();
  }

  /// Включает/выключает автопроверку сервисов после подключения.
  void setCheckServices(bool value) {
    if (value == _settings.checkServices) return;
    _settings = _settings.copyWith(checkServices: value);
    _notify();
    _scheduleSave();
  }

  /// Меняет тему: system/light/dark.
  void setThemeMode(String mode) {
    if (mode == _settings.themeMode) return;
    _settings = _settings.copyWith(themeMode: mode);
    _notify();
    _scheduleSave();
  }

  /// Обновляет поля подключения, которыми владеет TunnelController.
  ///
  /// Вызывается при изменении туннеля: так режим, TUN, страны и bind попадают в
  /// `gui.json` без второго источника истины.
  void updateConnection({
    required bool warpOnly,
    required bool tun,
    required List<String> countries,
    required String bind,
  }) {
    final same = _settings.warpOnly == warpOnly &&
        _settings.tun == tun &&
        _settings.bind == bind &&
        _listEquals(_settings.countries, countries);
    if (same) return;
    _settings = _settings.copyWith(
      warpOnly: warpOnly,
      tun: tun,
      countries: List<String>.unmodifiable(countries),
      bind: bind,
    );
    _scheduleSave();
  }

  bool _listEquals(List<String> a, List<String> b) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] != b[i]) return false;
    }
    return true;
  }

  void _scheduleSave() {
    _saveChain = _saveChain.then((_) => _save());
  }

  Future<void> _save() async {
    try {
      await store.save(_settings);
    } catch (error) {
      _error = 'Не удалось сохранить настройки: $error';
      _notify();
    }
  }

  bool _isProvider(String id) {
    for (final preset in _presets) {
      if (preset.id == id) return preset.kind == DnsKind.provider;
    }
    return false;
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
