/// Управление пайплайном `warpgen run` и его состоянием для интерфейса.
///
/// Контроллер не печатает и не завершает процесс сам: остановка — мягкая
/// (SIGINT, а для root-процесса под pkexec — файл-сигнал), чтобы Go успел
/// снять туннель и форвардер, а SIGKILL остаётся запасным вариантом по
/// таймауту.
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';

import '../models/journal_entry.dart';
import '../models/pipeline_event.dart';
import '../platform/process_runner.dart';
import '../services/warpgen_cli.dart';

/// Состояние туннеля в интерфейсе.
enum TunnelStatus {
  /// Пайплайн не запущен.
  idle,

  /// Идёт подготовка: скан, туннель, форвардер, Psiphon.
  starting,

  /// SOCKS5 поднят и работает.
  live,

  /// Останавливаем и ждём завершения процесса.
  stopping,

  /// Процесс завершился ошибкой.
  failed,
}

/// Контроллер одного прогона `warpgen run`.
class TunnelController extends ChangeNotifier {
  TunnelController({
    required this.cli,
    DateTime Function()? clock,
    bool warpOnly = true,
    bool tun = false,
    List<String>? countries,
    String bind = defaultBind,
    this.dnsResolver,
    this.ensureAccount,
  })  : _clock = clock ?? DateTime.now,
        _warpOnly = warpOnly,
        _tun = tun,
        _countries = List<String>.of(countries ?? defaultCountries),
        _bind = bind;

  /// Адрес SOCKS5 по умолчанию — как у CLI.
  static const defaultBind = '127.0.0.1:8086';

  /// Страны Psiphon по умолчанию — как у CLI.
  static const defaultCountries = ['NL', 'SE', 'DE', 'US', 'PL'];

  /// Рекомендуемые DNS (Comss, Malw, GeoHide) — если настройки недоступны.
  static const defaultDns = [
    'dns.comss.one',
    'dns.malw.link',
    'dns.geohide.ru',
  ];

  /// Сколько строк журнала держим в памяти.
  static const maxJournal = 200;

  final WarpgenCli cli;
  final DateTime Function() _clock;

  /// DNS для `run --dns`. Вызывается перед стартом: DNS провайдера может
  /// смениться, а список пресетов живёт в настройках.
  final Future<List<String>> Function()? dnsResolver;

  /// Гарантирует аккаунт WARP. Возвращает текст ошибки или `null`.
  final Future<String?> Function()? ensureAccount;

  TunnelStatus _status = TunnelStatus.idle;
  String _headline = 'Туннель выключен';
  String _detail = 'Подключитесь, чтобы получить локальный SOCKS5';
  String? _failure;
  String? _lastError;
  String? _lastStderr;
  String? _socks;
  String? _country;
  String? _ip;
  String? _org;
  String? _endpoint;
  String? _colo;
  String? _dns;
  int? _rttMs;
  DateTime? _readyAt;
  Duration? _prepareTime;
  bool _warpOnly = true;
  bool _tun = false;
  List<String> _countries = List<String>.of(defaultCountries);
  String _bind = defaultBind;
  final List<JournalEntry> _journal = [];
  RunningProcess? _process;
  Future<void>? _stdoutDone;
  Future<void>? _stderrDone;
  bool _stopRequested = false;
  bool _disposed = false;

  TunnelStatus get status => _status;

  /// Короткий заголовок состояния для карточки.
  String get headline => _headline;

  /// Пояснение под заголовком.
  String get detail => _detail;

  /// Текст ошибки, если прогон не удался.
  String? get failure => _failure;

  /// Адрес SOCKS5, который отдал пайплайн.
  String? get socks => _socks;

  /// Страна выхода (только в режиме с Psiphon).
  String? get country => _country;

  /// Внешний IP выхода.
  String? get ip => _ip;

  /// Организация выходного IP.
  String? get org => _org;

  /// Выбранный внешний endpoint.
  String? get endpoint => _endpoint;

  /// Узел Cloudflare.
  String? get colo => _colo;

  /// Задержка выбранного endpoint, мс.
  int? get rttMs => _rttMs;

  /// Резолвер, который выбрала цепочка (режим `--tun`).
  String? get dns => _dns;

  /// Когда цепочка стала готова.
  DateTime? get readyAt => _readyAt;

  /// Сколько заняла подготовка.
  Duration? get prepareTime => _prepareTime;

  /// Режим без Psiphon.
  bool get warpOnly => _warpOnly;

  /// Системный туннель (root): TUN-устройство и маршруты, весь трафик машины.
  ///
  /// Без него пайплайн поднимает только локальный SOCKS5 на 127.0.0.1 —
  /// адрес в интернете для программ, которые не используют прокси, не меняется.
  bool get tun => _tun;

  /// Включает или выключает системный туннель.
  void setTun(bool value) {
    if (isRunning || _tun == value) return;
    _tun = value;
    notifyListeners();
  }

  /// Страны Psiphon в порядке приоритета.
  List<String> get countries => List<String>.unmodifiable(_countries);

  /// Адрес SOCKS5, который получит warpgen.
  String get bind => _bind;

  /// Журнал: сначала старые, в конце самые свежие записи.
  List<JournalEntry> get journal => List<JournalEntry>.unmodifiable(_journal);

  /// Запущен ли процесс (включая подготовку и остановку).
  bool get isRunning =>
      _status == TunnelStatus.starting ||
      _status == TunnelStatus.live ||
      _status == TunnelStatus.stopping;

  /// Готова ли цепочка.
  bool get isLive => _status == TunnelStatus.live;

  /// Адрес прокси для копирования и показа.
  String? get proxyAddress => _socks == null ? null : 'socks5h://$_socks';

  /// Есть ли что исправлять в адресе SOCKS5.
  String? validateBind() {
    final value = _bind.trim();
    final match = RegExp(r'^127\.0\.0\.1:(\d{2,5})$').firstMatch(value);
    if (match == null) return 'Адрес должен быть 127.0.0.1:порт';
    final port = int.parse(match.group(1)!);
    if (port < 1 || port > 65535) return 'Порт вне диапазона 1–65535';
    return null;
  }

  /// Переключает режим (WARP или WARP + Psiphon).
  void setWarpOnly(bool value) {
    if (isRunning || _warpOnly == value) return;
    _warpOnly = value;
    notifyListeners();
  }

  /// Включает или выключает страну в списке приоритета.
  void toggleCountry(String code) {
    if (isRunning) return;
    final normalized = code.trim().toUpperCase();
    if (normalized.isEmpty) return;
    if (_countries.remove(normalized)) {
      notifyListeners();
      return;
    }
    _countries = [..._countries, normalized];
    notifyListeners();
  }

  /// Меняет адрес SOCKS5.
  void setBind(String value) {
    if (isRunning) return;
    _bind = value;
    notifyListeners();
  }

  /// Запускает пайплайн.
  Future<void> start() async {
    if (isRunning || _process != null) return;
    if (validateBind() != null) {
      _fail('Некорректный адрес SOCKS5: ${validateBind()}');
      return;
    }
    _resetRun();
    if (!_warpOnly && _countries.isEmpty) {
      _fail('Выберите хотя бы одну страну Psiphon');
      return;
    }
    _setStatus(
      TunnelStatus.starting,
      'Проверяем аккаунт…',
      'при первом включении WARP-аккаунт создаётся автоматически',
    );
    _addJournal(
      'Проверяем аккаунт WARP…',
      EventSeverity.info,
      isDiagnostic: true,
    );
    notifyListeners();

    try {
      final accountError = await ensureAccount?.call();
      if (_stopRequested) {
        _setStatus(TunnelStatus.idle, 'Туннель выключен', 'остановлено');
        notifyListeners();
        return;
      }
      if (accountError != null) {
        _fail(accountError);
        return;
      }

      _setStatus(
        TunnelStatus.starting,
        'Готовим DNS…',
        'проверяем выбранный DNS-резолвер',
      );
      notifyListeners();
      final dns = await (dnsResolver?.call() ?? Future.value(defaultDns));
      _addJournal('DNS: ${dns.join(', ')}', EventSeverity.info,
          isDiagnostic: true);
      if (_stopRequested) {
        _setStatus(TunnelStatus.idle, 'Туннель выключен', 'остановлено');
        notifyListeners();
        return;
      }

      _setStatus(
        TunnelStatus.starting,
        'Готовим туннель…',
        _warpOnly
            ? 'ищем endpoint и поднимаем WARP'
            : 'ищем endpoint, поднимаем WARP и Psiphon',
      );
      notifyListeners();

      final process = await cli.startRun(
        warpOnly: _warpOnly,
        countries: _countries,
        bind: _bind.trim(),
        tun: _tun,
        dns: dns,
      );
      _process = process;
      _stdoutDone = process.stdout.forEach(_onStdout);
      _stderrDone = process.stderr.forEach(_onStderr);
      unawaited(process.exitCode.then(_onExit));
      if (_stopRequested) process.sendInterrupt();
    } catch (error) {
      if (_stopRequested) {
        _setStatus(TunnelStatus.idle, 'Туннель выключен', 'остановлено');
        notifyListeners();
        return;
      }
      _fail(_describeStartError(error));
    }
  }

  /// Останавливает пайплайн: мягкий сигнал (для root — файл), затем SIGKILL
  /// по таймауту.
  Future<void> stop() async {
    _stopRequested = true;
    final process = _process;
    if (process == null) {
      _setStatus(TunnelStatus.idle, 'Туннель выключен', 'остановлено');
      notifyListeners();
      return;
    }
    _setStatus(
      TunnelStatus.stopping,
      'Останавливаем…',
      'снимаем туннель и форвардер',
    );
    notifyListeners();
    try {
      process.requestStop();
    } on FileSystemException catch (error) {
      // Мягкая остановка не удалась (например, нет доступа к файлу-сигналу).
      // Убиваем процесс принудительно, иначе он останется в _process и
      // повторное подключение будет молча игнорироваться.
      process.kill();
      _setStatus(
        TunnelStatus.failed,
        'Не удалось остановить',
        'процесс завершён принудительно',
      );
      _failure = 'не удалось остановить туннель: ${error.message}';
      _addJournal(_failure!, EventSeverity.error);
      notifyListeners();
      return;
    }
    try {
      await process.exitCode.timeout(const Duration(seconds: 10));
    } on TimeoutException {
      process.kill();
    }
  }

  /// Мягкая остановка при закрытии окна: ждать некому, но сигнала хватает.
  void shutdown() {
    final process = _process;
    if (process == null) return;
    _stopRequested = true;
    try {
      process.requestStop();
    } on FileSystemException {
      // Закрываемся: показать ошибку уже некому, остаётся SIGKILL по таймеру.
    }
    Timer(const Duration(seconds: 5), process.kill);
  }

  /// Запускает/останавливает одной кнопкой.
  Future<void> toggle() => isRunning ? stop() : start();

  @override
  void dispose() {
    _disposed = true;
    shutdown();
    super.dispose();
  }

  @override
  void notifyListeners() {
    // События процесса приходят асинхронно и могут опоздать к закрытому окну.
    if (_disposed) return;
    super.notifyListeners();
  }

  void _resetRun() {
    _failure = null;
    _lastError = null;
    _lastStderr = null;
    _socks = null;
    _country = null;
    _ip = null;
    _org = null;
    _endpoint = null;
    _colo = null;
    _dns = null;
    _rttMs = null;
    _readyAt = null;
    _prepareTime = null;
    _journal.clear();
    _stopRequested = false;
  }

  void _onStdout(String line) {
    final event = parsePipelineLine(line);
    if (event == null) {
      final text = line.trim();
      if (text.isNotEmpty) {
        _addJournal(text, EventSeverity.info, isDiagnostic: true);
      }
      return;
    }
    _handle(event);
  }

  void _onStderr(String line) {
    final text = line.trimRight();
    if (text.trim().isEmpty) return;
    // pkexec пишет причину отказа сюда (нет агента polkit, нет доступа к
    // бинарнику), а не в поток событий — показываем её в карточке ошибки.
    _lastStderr = text.trim();
    _addJournal(text, EventSeverity.info, isDiagnostic: true);
  }

  void _handle(PipelineEvent event) {
    _addJournal(event.description, event.severity);

    switch (event.kind) {
      case 'endpoint_selected':
        _endpoint = event.endpoint;
        _colo = event.colo;
        _rttMs = event.rttMs;
        _setStatus(
          TunnelStatus.starting,
          'Выбран endpoint',
          '${event.endpoint ?? '?'} · узел ${event.colo ?? '?'} · ${event.rttMs ?? 0} мс',
        );
        break;
      case 'outer_up':
        _setStatus(
          TunnelStatus.starting,
          'Туннель поднят',
          'проверяем трафик изнутри туннеля',
        );
        break;
      case 'outer_checked':
        _setStatus(
          TunnelStatus.starting,
          'Трафик через WARP идёт',
          'поднимаем UDP-форвардер',
        );
        break;
      case 'forwarder_up':
        _setStatus(
          TunnelStatus.starting,
          'Форвардер готов',
          'запускаем следующее звено',
        );
        break;
      case 'socks_up':
        _socks = event.socks;
        _setStatus(TunnelStatus.starting, 'SOCKS5 поднят', 'проверяем выход');
        break;
      case 'psiphon_starting':
        _setStatus(
          TunnelStatus.starting,
          'Поднимаем Psiphon',
          'страна ${event.country ?? '?'} — обычно это занимает до минуты',
        );
        break;
      case 'ready':
        _socks = event.socks ?? _socks;
        _country = event.country;
        _ip = event.ip;
        _org = event.org;
        _readyAt = _clock();
        if ((event.seconds ?? 0) > 0) {
          _prepareTime = Duration(seconds: event.seconds!.round());
        }
        _setStatus(TunnelStatus.live, 'Подключено', _liveDetail());
        break;
      case 'dns_selected':
        _dns = event.dns;
        break;
      case 'tun_up':
        // Системный туннель поднялся; маршруты применяются после ready.
        _setStatus(
          TunnelStatus.starting,
          'Системный туннель поднят',
          '${event.tun ?? 'TUN'}: настраиваем маршруты и DNS',
        );
        break;
      case 'routes_applied':
        _setStatus(TunnelStatus.live, 'Подключено', _liveDetail());
        break;
      case 'dns_failed':
        _detail = 'DNS не настроен: ${event.reason ?? 'причина неизвестна'}';
        break;
      case 'degraded':
        // Одиночная просадка не меняет звено — показываем, но не пугаем.
        if (_status == TunnelStatus.live) {
          _detail = 'просадка сети: ${event.reason ?? '?'}';
        }
        break;
      case 'failover':
        // Смена звена: мониторинг переключает туннель или страну, соединение
        // на несколько секунд просядет, но процесс продолжает работать.
        _detail = 'переключаемся: ${event.reason ?? '?'}';
        break;
      case 'stopped':
        if ((event.error ?? '').isNotEmpty) _lastError = event.error;
        break;
      default:
        break;
    }
    notifyListeners();
  }

  String _liveDetail() {
    final parts = <String>[];
    if ((_country ?? '').isNotEmpty) parts.add('страна $_country');
    if ((_ip ?? '').isNotEmpty) parts.add('IP $_ip');
    if ((_org ?? '').isNotEmpty) parts.add('$_org');
    if (_tun) parts.add('весь трафик машины идёт по цепочке');
    if (parts.isEmpty) return 'SOCKS5 готов';
    return parts.join(' · ');
  }

  Future<void> _onExit(int code) async {
    _process = null;
    await _drainStreams();
    if (_disposed) return;

    final stoppedByUser = _stopRequested || _status == TunnelStatus.stopping;
    if (code == 0) {
      _setStatus(
        TunnelStatus.idle,
        'Туннель выключен',
        stoppedByUser ? 'остановлено по команде' : 'процесс завершился',
      );
      notifyListeners();
      return;
    }
    if (stoppedByUser && _lastError == null) {
      _setStatus(
        TunnelStatus.idle,
        'Туннель выключен',
        'остановлено по команде',
      );
      notifyListeners();
      return;
    }
    // pkexec: 126 — пользователь отменил диалог, 127 — ошибка до или после
    // запроса пароля (нет агента polkit, недоступный бинарник и т.п.).
    if (_lastError == null && _tun && (code == 126 || code == 127)) {
      _fail(_rootFailure(code));
      return;
    }
    _fail(_lastError ?? 'warpgen завершился с кодом $code');
  }

  /// Объясняет отказ `pkexec` так, чтобы причину было видно в интерфейсе.
  String _rootFailure(int code) {
    if (code == 126) {
      return 'отказ в правах root: диалог пароля отменён';
    }
    final stderr = _lastStderr;
    if (stderr != null && stderr.contains('No authentication agent')) {
      return 'нет агента аутентификации polkit: запустите polkit-gnome, '
          'polkit-kde-agent или lxpolkit и повторите';
    }
    if (stderr != null && stderr.isNotEmpty) {
      return 'не удалось получить права root: $stderr';
    }
    return 'не удалось получить права root: проверьте журнал и повторите';
  }

  Future<void> _drainStreams() async {
    final pending = <Future<void>>[
      if (_stdoutDone != null) _stdoutDone!,
      if (_stderrDone != null) _stderrDone!,
    ];
    _stdoutDone = null;
    _stderrDone = null;
    if (pending.isEmpty) return;
    try {
      await Future.wait(pending).timeout(const Duration(seconds: 2));
    } on TimeoutException {
      // Потоки уже не нужны: процесс завершился.
    }
  }

  void _setStatus(TunnelStatus status, String headline, String detail) {
    _status = status;
    _headline = headline;
    _detail = detail;
  }

  void _fail(String message) {
    _status = TunnelStatus.failed;
    _failure = message;
    _headline = 'Не удалось подключиться';
    _detail = 'Проверьте журнал и повторите попытку';
    _addJournal(message, EventSeverity.error);
    notifyListeners();
  }

  void _addJournal(
    String text,
    EventSeverity severity, {
    bool isDiagnostic = false,
  }) {
    _journal.add(
      JournalEntry(
        time: _clock(),
        text: text,
        severity: severity,
        isDiagnostic: isDiagnostic,
      ),
    );
    if (_journal.length > maxJournal) {
      _journal.removeRange(0, _journal.length - maxJournal);
    }
  }

  String _describeStartError(Object error) {
    if (error is ProcessException) {
      return 'не удалось запустить ${cli.binaryPath}: ${error.message}';
    }
    return '$error';
  }
}
