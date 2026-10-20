/// Отчёт команды `warpgen scan --json`.
///
/// Go отдаёт поля без json-тегов, поэтому ключи заглавные (`IP`, `Port`, `RTT`
/// в наносекундах, `Status` числом), а `ranked` — уже отфильтрованные и
/// отсортированные по задержке endpoint'ы.
library;

/// Итог проверки одного endpoint (совпадает с `scanner.Status`).
enum ScanStatus {
  ok,
  noHandshake,
  noTrace,
  badRegion,
  excludedColo,
  torn,
  unknown,
}

/// Один проверенный endpoint.
class ScanHit {
  const ScanHit({
    required this.ip,
    required this.port,
    this.loc = '',
    this.colo = '',
    this.rtt = Duration.zero,
    this.loss = 0,
    this.torn = false,
    this.status = ScanStatus.unknown,
  });

  factory ScanHit.fromJson(Map<String, Object?> json) {
    return ScanHit(
      ip: '${json['IP'] ?? ''}',
      port: _int(json['Port']) ?? 0,
      loc: '${json['Loc'] ?? ''}',
      colo: '${json['Colo'] ?? ''}',
      rtt: Duration(microseconds: (_int(json['RTT']) ?? 0) ~/ 1000),
      loss: _double(json['Loss']) ?? 0,
      torn: json['Torn'] == true,
      status: _status(_int(json['Status'])),
    );
  }

  final String ip;
  final int port;
  final String loc;
  final String colo;
  final Duration rtt;
  final double loss;
  final bool torn;
  final ScanStatus status;

  /// `ip:port` — та же строка, что в таблице CLI.
  String get endpoint => '$ip:$port';

  /// Задержка внутри туннеля: `42 мс`, либо `—`, если её не мерили.
  String get rttLabel =>
      rtt.inMilliseconds > 0 ? '${rtt.inMilliseconds} мс' : '—';

  /// Регион, который видит внешний сервис.
  String get locLabel => loc.trim().isEmpty ? '?' : loc.trim();

  /// Узел Cloudflare.
  String get coloLabel => colo.trim().isEmpty ? '?' : colo.trim();

  /// Потери внутри туннеля в процентах.
  String get lossLabel => '${(loss * 100).round()}%';

  String get statusLabel {
    switch (status) {
      case ScanStatus.ok:
        return 'OK';
      case ScanStatus.noHandshake:
        return 'нет хендшейка';
      case ScanStatus.noTrace:
        return 'нет данных через туннель';
      case ScanStatus.badRegion:
        return 'регион RU';
      case ScanStatus.excludedColo:
        return 'исключённый colo';
      case ScanStatus.torn:
        return 'туннель рвётся';
      case ScanStatus.unknown:
        return 'неизвестно';
    }
  }

  /// Подходит ли endpoint (статус OK).
  bool get isGood => status == ScanStatus.ok;
}

/// Полный отчёт скана: сколько кандидатов проверили, что нашли и что подошло.
class ScanReport {
  const ScanReport({
    required this.candidates,
    required this.results,
    required this.ranked,
  });

  factory ScanReport.fromJson(Map<String, Object?> json) {
    return ScanReport(
      candidates: _int(json['candidates']) ?? 0,
      results: _hits(json['results']),
      ranked: _hits(json['ranked']),
    );
  }

  final int candidates;
  final List<ScanHit> results;
  final List<ScanHit> ranked;

  int get checked => results.length;
}

List<ScanHit> _hits(Object? value) {
  if (value is! List<Object?>) return const [];
  return value
      .whereType<Map<Object?, Object?>>()
      .map((item) => ScanHit.fromJson(item.cast<String, Object?>()))
      .toList(growable: false);
}

ScanStatus _status(int? value) {
  switch (value) {
    case 0:
      return ScanStatus.ok;
    case 1:
      return ScanStatus.noHandshake;
    case 2:
      return ScanStatus.noTrace;
    case 3:
      return ScanStatus.badRegion;
    case 4:
      return ScanStatus.excludedColo;
    case 5:
      return ScanStatus.torn;
    default:
      return ScanStatus.unknown;
  }
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
