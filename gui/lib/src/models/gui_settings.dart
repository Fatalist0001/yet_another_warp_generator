/// Настройки GUI, которые переживают перезапуск.
///
/// Файл `~/.warpgen/gui.json` не содержит ключей аккаунта: только выбор
/// пользователя. Приватный ключ WARP остаётся в файле аккаунта CLI.
library;

import 'package:flutter/material.dart';

import '../controllers/tunnel_controller.dart';

/// Страны Psiphon по умолчанию — как у CLI.
const defaultCountries = TunnelController.defaultCountries;

/// Настройки интерфейса.
class GuiSettings {
  const GuiSettings({
    this.warpOnly = true,
    this.tun = false,
    this.countries = defaultCountries,
    this.bind = TunnelController.defaultBind,
    this.dnsId = 'recommended',
    this.customDns = '',
    this.checkServices = true,
    this.themeMode = 'system',
  });

  factory GuiSettings.fromJson(Map<String, Object?> json) {
    final countries = <String>[];
    final rawCountries = json['countries'];
    if (rawCountries is List) {
      for (final item in rawCountries) {
        final value = '$item'.trim().toUpperCase();
        if (value.isNotEmpty) countries.add(value);
      }
    }
    return GuiSettings(
      warpOnly: json['warp_only'] is bool ? json['warp_only'] as bool : true,
      tun: json['tun'] is bool ? json['tun'] as bool : false,
      countries: countries.isEmpty ? defaultCountries : countries,
      bind: _string(json['bind'], TunnelController.defaultBind),
      dnsId: _string(json['dns_id'], 'recommended'),
      customDns: _string(json['custom_dns'], ''),
      checkServices: json['check_services'] is bool
          ? json['check_services'] as bool
          : true,
      themeMode: _theme(json['theme_mode']),
    );
  }

  final bool warpOnly;
  final bool tun;
  final List<String> countries;
  final String bind;
  final String dnsId;
  final String customDns;
  final bool checkServices;

  /// `system`, `light` или `dark`.
  final String themeMode;

  ThemeMode get theme {
    switch (themeMode) {
      case 'light':
        return ThemeMode.light;
      case 'dark':
        return ThemeMode.dark;
      default:
        return ThemeMode.system;
    }
  }

  GuiSettings copyWith({
    bool? warpOnly,
    bool? tun,
    List<String>? countries,
    String? bind,
    String? dnsId,
    String? customDns,
    bool? checkServices,
    String? themeMode,
  }) {
    return GuiSettings(
      warpOnly: warpOnly ?? this.warpOnly,
      tun: tun ?? this.tun,
      countries: countries ?? this.countries,
      bind: bind ?? this.bind,
      dnsId: dnsId ?? this.dnsId,
      customDns: customDns ?? this.customDns,
      checkServices: checkServices ?? this.checkServices,
      themeMode: themeMode ?? this.themeMode,
    );
  }

  Map<String, Object?> toJson() => {
        'version': 1,
        'warp_only': warpOnly,
        'tun': tun,
        'countries': countries,
        'bind': bind,
        'dns_id': dnsId,
        'custom_dns': customDns,
        'check_services': checkServices,
        'theme_mode': themeMode,
      };
}

String _string(Object? value, String fallback) {
  final text = '${value ?? ''}'.trim();
  return text.isEmpty ? fallback : text;
}

String _theme(Object? value) {
  final text = '$value';
  return text == 'light' || text == 'dark' || text == 'system'
      ? text
      : 'system';
}
