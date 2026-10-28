/// Канал трея Windows.
///
/// Иконку и меню создаёт runner (C++), Dart получает команды меню и просит
/// свернуть или вернуть окно. На Linux, Android и в тестах плагина нет, поэтому
/// вызовы становятся тихим no-op: интерфейс не должен зависеть от трея.
library;

import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// Связь с иконкой в трее.
class TrayChannel {
  TrayChannel({MethodChannel? channel, bool? supported})
      : _channel = channel ?? const MethodChannel(channelName),
        _supported = supported ?? Platform.isWindows;

  /// Имя канала; то же значение в C++ (`kTrayChannelName`).
  static const String channelName = 'ru.vernette.warpgen/tray';

  final MethodChannel _channel;
  final bool _supported;
  bool _handlerInstalled = false;

  /// Есть ли трей на этой платформе.
  bool get isSupported => _supported;

  /// Свернуть окно в трей.
  Future<void> minimizeToTray() => _invoke('minimizeToTray');

  /// Вернуть окно из трея.
  Future<void> restoreFromTray() => _invoke('restoreFromTray');

  /// Попросить runner закрыть приложение.
  ///
  /// Вызывается только после остановки туннеля: выход с применёнными
  /// маршрутами оставил бы машину без сети до перезагрузки.
  Future<void> quit() => _invoke('quit');

  /// Подписаться на команды меню трея.
  ///
  /// Обработчик ставится один раз: `_Root` пересоздаёт контроллеры при смене
  /// бинарника, и повторная подписка только плодила бы копии.
  void setupTrayHandlers({
    required Future<void> Function() onQuit,
    Future<void> Function()? onStop,
  }) {
    if (!_supported || _handlerInstalled) return;
    _handlerInstalled = true;
    _channel.setMethodCallHandler((call) async {
      switch (call.method) {
        case 'trayQuit':
          await onQuit();
          break;
        case 'trayStopTunnel':
          await onStop?.call();
          break;
        default:
          break;
      }
    });
  }

  /// Снять обработчик команд (при выходе приложения).
  void dispose() {
    if (!_handlerInstalled) return;
    _handlerInstalled = false;
    _channel.setMethodCallHandler(null);
  }

  Future<void> _invoke(String method) async {
    if (!_supported) return;
    try {
      await _channel.invokeMethod<void>(method);
    } on MissingPluginException {
      // Плагина нет: тесты, Linux, Android или сборка runner'а без трея.
    } on PlatformException catch (error) {
      debugPrint('трей: $method — ${error.message ?? error.code}');
    }
  }
}
