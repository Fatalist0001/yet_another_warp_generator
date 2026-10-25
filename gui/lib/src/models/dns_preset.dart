/// DNS-пресеты и DNS текущего подключения.
///
/// Список приходит из `warpgen dns-presets --json`: единственный источник —
/// Go-пакет `config`, GUI только отображает значения.
library;

/// Тип значения DNS-пресета.
enum DnsKind {
  ip,
  hostname,
  group,
  provider,
  custom;

  static DnsKind parse(Object? value) {
    switch ('$value') {
      case 'ip':
        return DnsKind.ip;
      case 'hostname':
        return DnsKind.hostname;
      case 'group':
        return DnsKind.group;
      case 'provider':
        return DnsKind.provider;
      case 'custom':
        return DnsKind.custom;
      default:
        return DnsKind.ip;
    }
  }
}

/// Один DNS-пресет.
class DnsPreset {
  const DnsPreset({
    required this.id,
    required this.label,
    required this.value,
    required this.kind,
    this.note = '',
    this.isDefault = false,
    this.resolvedIp = '',
    this.error = '',
  });

  factory DnsPreset.fromJson(Map<String, Object?> json) {
    return DnsPreset(
      id: '${json['id'] ?? ''}',
      label: '${json['label'] ?? ''}',
      value: '${json['value'] ?? ''}',
      kind: DnsKind.parse(json['kind']),
      note: '${json['note'] ?? ''}',
      isDefault: json['default'] == true,
      resolvedIp: '${json['resolved_ip'] ?? ''}',
      error: '${json['error'] ?? ''}',
    );
  }

  final String id;
  final String label;
  final String value;
  final DnsKind kind;
  final String note;
  final bool isDefault;

  /// Результат `--resolve`: IP или список IP через запятую.
  final String resolvedIp;

  /// Ошибка резолва hostname'а.
  final String error;

  /// Отдельные значения для `run --dns`.
  List<String> get values => value
      .split(',')
      .map((part) => part.trim())
      .where((part) => part.isNotEmpty)
      .toList(growable: false);

  DnsPreset withResolution(String resolved, String resolveError) {
    return DnsPreset(
      id: id,
      label: label,
      value: value,
      kind: kind,
      note: note,
      isDefault: isDefault,
      resolvedIp: resolved,
      error: resolveError,
    );
  }
}

/// DNS текущего подключения, который отдаёт `warpgen dns-current --json`.
class DnsCurrent {
  const DnsCurrent({
    required this.interfaceName,
    required this.servers,
    this.warning = '',
  });

  factory DnsCurrent.fromJson(Map<String, Object?> json) {
    final servers = <String>[];
    final raw = json['servers'];
    if (raw is List) {
      for (final item in raw) {
        final value = '$item'.trim();
        if (value.isNotEmpty) {
          servers.add(value);
        }
      }
    }
    return DnsCurrent(
      interfaceName: '${json['interface'] ?? ''}',
      servers: servers,
      warning: '${json['warning'] ?? ''}',
    );
  }

  final String interfaceName;
  final List<String> servers;
  final String warning;
}
