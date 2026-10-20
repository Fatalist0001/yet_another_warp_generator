/// Строка журнала: событие пайплайна или диагностика CLI.
library;

import 'pipeline_event.dart';

/// Запись журнала, которую видит пользователь.
class JournalEntry {
  const JournalEntry({
    required this.time,
    required this.text,
    this.severity = EventSeverity.info,
    this.isDiagnostic = false,
  });

  final DateTime time;
  final String text;
  final EventSeverity severity;

  /// Строка пришла из stderr (служебный вывод), а не из потока событий.
  final bool isDiagnostic;
}
