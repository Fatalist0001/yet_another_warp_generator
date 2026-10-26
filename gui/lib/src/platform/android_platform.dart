/// Мост к Android-обёртке: VpnService, fd TUN и пути приложения.
///
/// На Android CLI нельзя запустить под pkexec: TUN создаёт `VpnService`, а
/// готовый дескриптор передаётся Go-процессу по unix-сокету. Поток событий
/// приходит из Kotlin теми же строками NDJSON, что и stdout CLI на Linux.
library;

import 'package:flutter/services.dart';

/// Пути, которые Android даёт приложению.
class AndroidPaths {
  const AndroidPaths({
    required this.nativeLibraryDir,
    required this.filesDir,
    required this.cacheDir,
  });

  final String nativeLibraryDir;
  final String filesDir;
  final String cacheDir;

  String get binaryPath => '$nativeLibraryDir/libwarpgen.so';
}

/// Каналы связи с `MainActivity` и `WarpgenVpnService`.
class AndroidPlatform {
  static const MethodChannel _methods = MethodChannel('warpgen/android');
  static const EventChannel _events = EventChannel('warpgen/events');

  static Future<AndroidPaths> paths() async {
    final raw = await _methods.invokeMapMethod<String, Object?>('getPaths');
    if (raw == null) {
      throw PlatformException(
        code: 'paths',
        message: 'Android не вернул пути приложения',
      );
    }
    return AndroidPaths(
      nativeLibraryDir: '${raw['nativeLibraryDir'] ?? ''}',
      filesDir: '${raw['filesDir'] ?? ''}',
      cacheDir: '${raw['cacheDir'] ?? ''}',
    );
  }

  static Future<void> startVpn(List<String> args) {
    return _methods.invokeMethod<void>('startVpn', {'args': args});
  }

  static Future<void> stopVpn({bool force = false}) {
    return _methods.invokeMethod<void>(
      force ? 'forceStopVpn' : 'stopVpn',
    );
  }

  static Stream<Map<String, Object?>> get events {
    return _events.receiveBroadcastStream().map((event) {
      if (event is Map) return event.cast<String, Object?>();
      return <String, Object?>{};
    });
  }
}
