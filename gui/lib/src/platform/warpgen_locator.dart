/// Поиск бинарника `warpgen` и файла аккаунта.
///
/// GUI не вшивает ядро в себя: он запускает тот же CLI-бинарник, что и
/// пользователь. Порядок поиска:
/// 1. путь, указанный вручную (диалог «Где warpgen»);
/// 2. переменная окружения `WARPGEN_BIN`;
/// 3. `warpgen` в `PATH`;
/// 4. `./warpgen` в текущем каталоге (запуск из репозитория через `make build`).
///
/// Файл аккаунта по умолчанию лежит рядом с бинарником — так же его создаёт
/// `warpgen register` при запуске из каталога инструмента. Переопределяется
/// переменной `WARPGEN_ACCOUNT`.
library;

import 'dart:io';

/// Найденный бинарник и производные пути.
class WarpgenLocation {
  const WarpgenLocation({
    required this.binaryPath,
    required this.accountFile,
    required this.source,
  });

  final String binaryPath;

  /// Файл аккаунта, который GUI передаёт во все команды через `--account`.
  final String accountFile;

  /// Откуда взят путь — для подписи в интерфейсе.
  final String source;

  /// Каталог бинарника (рядом лежат аккаунт и warp-plus).
  String get binaryDir => File(binaryPath).parent.path;
}

/// Ищет установленный `warpgen`.
class WarpgenLocator {
  WarpgenLocator({
    Map<String, String>? environment,
    Directory? workingDirectory,
    bool Function(String path)? fileExists,
  })  : _environment = environment ?? Platform.environment,
        _workingDirectory = workingDirectory ?? Directory.current,
        _fileExists = fileExists ?? _defaultFileExists;

  static const binaryEnv = 'WARPGEN_BIN';
  static const accountEnv = 'WARPGEN_ACCOUNT';
  static const defaultAccountName = 'warpgen-account.json';

  final Map<String, String> _environment;
  final Directory _workingDirectory;
  final bool Function(String path) _fileExists;

  /// Возвращает найденный бинарник или `null`, если ничего не подошло.
  Future<WarpgenLocation?> locate({String? explicitPath}) async {
    final candidates = <({String path, String source})>[
      if (_filled(explicitPath))
        (path: explicitPath!.trim(), source: 'указано вручную'),
      if (_filled(_environment[binaryEnv]))
        (path: _environment[binaryEnv]!.trim(), source: binaryEnv),
      for (final dir in _pathDirs())
        (path: '$dir${Platform.pathSeparator}warpgen', source: 'PATH'),
      (
        path: '${_workingDirectory.path}${Platform.pathSeparator}warpgen',
        source: 'текущий каталог',
      ),
    ];

    for (final candidate in candidates) {
      if (!_fileExists(candidate.path)) continue;
      return WarpgenLocation(
        binaryPath: candidate.path,
        accountFile: _resolveAccountFile(candidate.path),
        source: candidate.source,
      );
    }
    return null;
  }

  String _resolveAccountFile(String binaryPath) {
    final fromEnv = _environment[accountEnv];
    if (_filled(fromEnv)) return fromEnv!.trim();
    final dir = File(binaryPath).parent.path;
    return '$dir${Platform.pathSeparator}$defaultAccountName';
  }

  Iterable<String> _pathDirs() sync* {
    final path = _environment['PATH'] ?? '';
    for (final dir in path.split(':')) {
      if (dir.trim().isNotEmpty) yield dir.trim();
    }
  }
}

bool _filled(String? value) => value != null && value.trim().isNotEmpty;

bool _defaultFileExists(String path) {
  try {
    return File(path).existsSync();
  } on FileSystemException {
    return false;
  }
}
