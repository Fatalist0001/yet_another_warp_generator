/// Результаты проверки сервисов через SOCKS5 (`warpgen services --json`).
library;

/// Категория сервиса.
enum ServiceCategory {
  ru,
  blocked;

  static ServiceCategory parse(Object? value) {
    return '$value' == 'blocked' ? ServiceCategory.blocked : ServiceCategory.ru;
  }
}

/// Состояние одной проверки.
enum ServiceState {
  ok,
  slow,
  httpError,
  timeout,
  networkError;

  static ServiceState parse(Object? value) {
    switch ('$value') {
      case 'ok':
        return ServiceState.ok;
      case 'slow':
        return ServiceState.slow;
      case 'http_error':
        return ServiceState.httpError;
      case 'timeout':
        return ServiceState.timeout;
      default:
        return ServiceState.networkError;
    }
  }

  bool get isOk => this == ServiceState.ok || this == ServiceState.slow;
}

/// Результат проверки одного сервиса.
class ServiceStatus {
  const ServiceStatus({
    required this.id,
    required this.label,
    required this.category,
    required this.state,
    this.code = 0,
    this.rttMs = 0,
    this.error = '',
  });

  factory ServiceStatus.fromJson(Map<String, Object?> json) {
    return ServiceStatus(
      id: '${json['id'] ?? ''}',
      label: '${json['label'] ?? ''}',
      category: ServiceCategory.parse(json['category']),
      state: ServiceState.parse(json['status']),
      code: _int(json['code']),
      rttMs: _int(json['rtt_ms']),
      error: '${json['error'] ?? ''}',
    );
  }

  final String id;
  final String label;
  final ServiceCategory category;
  final ServiceState state;
  final int code;
  final int rttMs;
  final String error;

  /// Сервис ответил успешно (возможно, медленно).
  bool get isOk => state.isOk;

  /// Сервер вообще ответил. HTTP-ошибка (403/429/5xx) сюда входит: сеть до
  /// сервиса есть, но он не пустил или сломался. Красным такие не отмечаем.
  bool get isReachable =>
      state != ServiceState.timeout && state != ServiceState.networkError;

  /// Сервер ответил HTTP-ошибкой — это предупреждение, а не полная недоступность.
  bool get isHttpError => state == ServiceState.httpError;

  /// Совсем недоступен: нет ответа (таймаут или сетевая ошибка).
  bool get isUnavailable => !isReachable;
}

/// Отчёт целиком.
class ServiceReport {
  const ServiceReport({required this.socks, required this.services});

  factory ServiceReport.fromJson(Map<String, Object?> json) {
    final services = <ServiceStatus>[];
    final raw = json['services'];
    if (raw is List) {
      for (final item in raw) {
        if (item is Map) {
          services.add(ServiceStatus.fromJson(item.cast<String, Object?>()));
        }
      }
    }
    return ServiceReport(
      socks: '${json['socks'] ?? ''}',
      services: services,
    );
  }

  final String socks;
  final List<ServiceStatus> services;
}

int _int(Object? value) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  return int.tryParse('$value') ?? 0;
}
