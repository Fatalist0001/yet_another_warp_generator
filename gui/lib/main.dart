// GUI warpgen: Material You-интерфейс к локальному CLI.
//
// Приложение не вшивает ядро: оно запускает бинарник `warpgen` и читает его
// машинный вывод (`--json`). На Linux и Windows туннель остаётся userspace,
// на Android TUN создаёт VpnService и передаёт fd тому же CLI.

import 'dart:io';

import 'package:flutter/material.dart';

import 'src/app.dart';
import 'src/platform/android_platform.dart';
import 'src/platform/android_process_runner.dart';
import 'src/platform/app_paths.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  if (Platform.isAndroid) {
    final paths = await AndroidPlatform.paths();
    final environment = <String, String>{
      ...Platform.environment,
      'HOME': paths.filesDir,
      'WARPGEN_BIN': paths.binaryPath,
      'WARPGEN_ACCOUNT': '${paths.filesDir}/.warpgen/warpgen-account.json',
    };
    runApp(
      WarpgenApp(
        environment: environment,
        runner: const AndroidProcessRunner(),
      ),
    );
    return;
  }
  // Windows-сборка портативная: данные лежат в warpgen-data рядом с exe.
  // Если рядом писать нельзя, resolveAppPaths возвращает ~/.warpgen.
  String? dataDir;
  if (Platform.isWindows) {
    final paths = await resolveAppPaths(
      isWindows: true,
      homeDir: homeDirectory(Platform.environment),
      executablePath: Platform.resolvedExecutable,
    );
    dataDir = paths.dataDir;
  }
  runApp(WarpgenApp(dataDir: dataDir));
}
