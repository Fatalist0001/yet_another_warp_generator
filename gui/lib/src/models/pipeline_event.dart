/// Событие пайплайна `warpgen run`.
///
/// CLI отдаёт их потоком NDJSON (`--json`) — по одному объекту в строке. Имена
/// событий — публичный контракт CLI, поэтому неизвестные типы не считаем ошибкой:
/// они просто попадают в журнал как есть.
library;

import 'dart:convert';

/// Важность события для оформления журнала.
enum EventSeverity { info, warning, error, success }

/// Событие пайплайна (endpoint, туннель, форвардер, готовность, сбои).
class PipelineEvent {
  const PipelineEvent({
    required this.kind,
    required this.time,
    this.endpoint,
    this.colo,
    this.region,
    this.rttMs,
    this.forwarder,
    this.socks,
    this.country,
    this.ip,
    this.org,
    this.tun,
    this.mtu,
    this.routes,
    this.dns,
    this.seconds,
    this.reason,
    this.fails,
    this.error,
  });

  /// Разбирает один объект события. Отсутствующие поля остаются пустыми:
  /// CLI заполняет только то, что относится к типу события.
  factory PipelineEvent.fromJson(Map<String, Object?> json) {
    return PipelineEvent(
      kind: _string(json['event']),
      time: _parseTime(json['time']),
      endpoint: _optional(json['endpoint']),
      colo: _optional(json['colo']),
      region: _optional(json['region']),
      rttMs: _int(json['rtt_ms']),
      forwarder: _optional(json['forwarder']),
      socks: _optional(json['socks']),
      country: _optional(json['country']),
      ip: _optional(json['ip']),
      org: _optional(json['org']),
      tun: _optional(json['tun']),
      mtu: _int(json['mtu']),
      routes: _int(json['routes']),
      dns: _optional(json['dns']),
      seconds: _double(json['seconds']),
      reason: _optional(json['reason']),
      fails: _int(json['fails']),
      error: _optional(json['error']),
    );
  }

  /// Тип события (`ready`, `failover`, …).
  final String kind;

  /// Момент события в локальном времени.
  final DateTime time;

  final String? endpoint;
  final String? colo;
  final String? region;
  final int? rttMs;
  final String? forwarder;
  final String? socks;
  final String? country;
  final String? ip;
  final String? org;
  final String? tun;
  final int? mtu;
  final int? routes;
  final String? dns;
  final double? seconds;
  final String? reason;
  final int? fails;
  final String? error;

  /// Готов ли прокси (событие `ready`).
  bool get isReady => kind == 'ready';

  /// Остановлен ли пайплайн (последнее событие потока).
  bool get isStopped => kind == 'stopped';

  /// Цветовая метка журнала.
  EventSeverity get severity {
    switch (kind) {
      case 'ready':
        return EventSeverity.success;
      case 'stopped':
        return (error ?? '').isNotEmpty
            ? EventSeverity.error
            : EventSeverity.info;
      case 'degraded':
      case 'dns_failed':
        return EventSeverity.warning;
      case 'failover':
        return EventSeverity.warning;
      default:
        return EventSeverity.info;
    }
  }

  /// Человекочитаемая строка — как `String()` у события в CLI.
  String get description {
    switch (kind) {
      case 'endpoint_selected':
        final parts = <String>['выбран endpoint ${_dash(endpoint)}'];
        if (_filled(colo)) parts.add('узел $colo');
        if (_filled(region)) parts.add('регион $region');
        if ((rttMs ?? 0) > 0) parts.add('$rttMs мс');
        return parts.join(', ');
      case 'outer_up':
        return 'внешний туннель поднят: ${_dash(endpoint)}';
      case 'outer_checked':
        return 'трафик через туннель проверен (warp=on, узел ${_dash(colo)})';
      case 'forwarder_up':
        return 'UDP-форвардер слушает ${_dash(forwarder)}';
      case 'socks_up':
        return 'SOCKS5 без Psiphon слушает ${_dash(socks)} (трафик идёт через WARP)';
      case 'psiphon_starting':
        return 'страна ${_dash(country)}: запускаем warp-plus с Psiphon';
      case 'ready':
        final tail = <String>[];
        if (_filled(org)) tail.add(org!);
        if ((seconds ?? 0) > 0) tail.add('подготовка ${_seconds(seconds!)}');
        return 'готово: socks5://${_dash(socks)}, страна ${_dash(country)}, '
            'IP ${_dash(ip)}${tail.isEmpty ? '' : ' (${tail.join(', ')})'}';
      case 'tun_up':
        return 'системный туннель поднят: ${_dash(tun)}, MTU ${mtu ?? 0}';
      case 'routes_applied':
        return 'маршруты применены: ${_dash(tun)}, команд ${routes ?? 0}';
      case 'routes_reverted':
        return 'маршруты и DNS сняты';
      case 'dns_selected':
        if ((rttMs ?? 0) > 0) {
          return 'DNS через цепочку: ${_dash(dns)} ($rttMs мс)';
        }
        return 'DNS через цепочку: ${_dash(dns)}';
      case 'dns_failed':
        return 'DNS не настроен: ${_dash(reason)}';
      case 'degraded':
        return 'просадка (сбой ${fails ?? 0} подряд): ${_dash(reason)}';
      case 'failover':
        return 'смена звена: ${_dash(reason)}';
      case 'stopped':
        return _filled(error) ? 'остановлено: $error' : 'остановлено';
      default:
        return kind;
    }
  }
}

/// Разбирает строку NDJSON. Возвращает `null`, если строка не JSON-объект —
/// такие строки попадают в журнал как диагностика.
PipelineEvent? parsePipelineLine(String line) {
  final trimmed = line.trim();
  if (trimmed.isEmpty || !trimmed.startsWith('{')) return null;
  try {
    final decoded = jsonDecode(trimmed);
    if (decoded is! Map<String, Object?>) return null;
    return PipelineEvent.fromJson(decoded);
  } on FormatException {
    return null;
  }
}

bool _filled(String? value) => value != null && value.trim().isNotEmpty;

String _string(Object? value) => value is String ? value : '${value ?? ''}';

String? _optional(Object? value) {
  if (value == null) return null;
  final text = '$value'.trim();
  return text.isEmpty ? null : text;
}

int? _int(Object? value) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  return int.tryParse('${value ?? ''}');
}

double? _double(Object? value) {
  if (value is double) return value;
  if (value is num) return value.toDouble();
  return double.tryParse('${value ?? ''}');
}

String _dash(String? value) => _filled(value) ? value!.trim() : '?';

String _seconds(double value) {
  final total = value.round();
  if (total < 60) return '$total с';
  final minutes = total ~/ 60;
  final seconds = total % 60;
  return '$minutes мин $seconds с';
}

/// Go отдаёт время в RFC3339Nano (до 9 знаков после точки), а Dart понимает
/// не больше шести — лишние цифры отбрасываем.
DateTime _parseTime(Object? value) {
  final raw = value?.toString().trim() ?? '';
  if (raw.isEmpty) return DateTime.now();
  final normalized = raw.replaceFirstMapped(RegExp(r'\.(\d+)'), (match) {
    final fraction = match.group(1)!;
    final trimmed = fraction.length > 6
        ? fraction.substring(0, 6)
        : fraction.padRight(6, '0');
    return '.$trimmed';
  });
  return DateTime.tryParse(normalized)?.toLocal() ?? DateTime.now();
}
