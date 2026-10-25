/// Терминальный блок состояния: события пайплайна вместо длинного журнала.
library;

import 'package:flutter/material.dart';

import '../controllers/tunnel_controller.dart';
import '../models/journal_entry.dart';
import '../models/pipeline_event.dart';
import 'ui.dart';

/// Компактный терминал с автопрокруткой и копированием строк.
class TerminalPanel extends StatefulWidget {
  const TerminalPanel({super.key, required this.tunnel});

  final TunnelController tunnel;

  @override
  State<TerminalPanel> createState() => _TerminalPanelState();
}

class _TerminalPanelState extends State<TerminalPanel> {
  final ScrollController _scroll = ScrollController();
  bool _stickToBottom = true;
  int _lastCount = 0;

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      // Только журнал: события статуса и конфигурации не должны перестраивать
      // терминал, а новые строки — всю страницу подключения.
      animation: widget.tunnel.journalRevision,
      builder: (context, _) {
        final entries = widget.tunnel.journal;
        // Автопрокрутку планируем только когда появилась новая запись, а не
        // на каждое событие: иначе прокрутка и перестройка списка конкурируют.
        if (entries.length != _lastCount) {
          _lastCount = entries.length;
          WidgetsBinding.instance.addPostFrameCallback((_) => _scrollToBottom());
        }
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        return Container(
          decoration: BoxDecoration(
            color: scheme.surfaceContainerHighest,
            borderRadius: BorderRadius.circular(16),
            border: Border.all(color: scheme.outlineVariant),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 8, 4),
                child: Row(
                  children: [
                    Text(
                      'Терминал',
                      style: theme.textTheme.labelLarge?.copyWith(
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                    const Spacer(),
                    TextButton(
                      onPressed: entries.isEmpty
                          ? null
                          : () => copyToClipboard(
                                context,
                                _asText(entries),
                                message: 'Состояние скопировано',
                              ),
                      style: TextButton.styleFrom(
                        visualDensity: VisualDensity.compact,
                      ),
                      child: const Text('Копировать'),
                    ),
                  ],
                ),
              ),
              Divider(height: 1, color: scheme.outlineVariant),
              SizedBox(
                height: 250,
                child: entries.isEmpty
                    ? Center(
                        child: Text(
                          '› ожидание подключения',
                          style: _terminalStyle(
                            theme,
                            color: scheme.onSurfaceVariant,
                          ),
                        ),
                      )
                    : NotificationListener<ScrollNotification>(
                        onNotification: (notification) {
                          if (notification.metrics.axis == Axis.vertical) {
                            _stickToBottom = notification.metrics.pixels >=
                                notification.metrics.maxScrollExtent - 8;
                          }
                          return false;
                        },
                        child: ListView.builder(
                          controller: _scroll,
                          padding: const EdgeInsets.symmetric(
                            horizontal: 14,
                            vertical: 10,
                          ),
                          itemCount: entries.length,
                          itemBuilder: (context, index) {
                            return _Line(entry: entries[index]);
                          },
                        ),
                      ),
              ),
            ],
          ),
        );
      },
    );
  }

  void _scrollToBottom() {
    if (!_stickToBottom || !_scroll.hasClients) return;
    final max = _scroll.position.maxScrollExtent;
    if (_scroll.position.pixels < max) {
      _scroll.jumpTo(max);
    }
  }

  String _asText(List<JournalEntry> entries) {
    return entries
        .map((entry) => '${formatTime(entry.time)}  ${entry.text}')
        .join('\n');
  }
}

/// Единый стиль терминала: моноширинный, но крупнее и плотнее системного
/// `monospace`, который выглядел слишком тонким рядом с остальным интерфейсом.
TextStyle? _terminalStyle(
  ThemeData theme, {
  Color? color,
  FontWeight fontWeight = FontWeight.w500,
}) {
  return theme.textTheme.bodyMedium?.copyWith(
    fontFamily: monoFontFamily,
    fontSize: 13.5,
    height: 1.35,
    fontWeight: fontWeight,
    color: color,
  );
}

class _Line extends StatelessWidget {
  const _Line({required this.entry});

  final JournalEntry entry;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final color = severityColor(scheme, entry.severity);
    final marker = switch (entry.severity) {
      EventSeverity.error => '✗',
      EventSeverity.warning => '!',
      EventSeverity.success => '✓',
      EventSeverity.info => entry.isDiagnostic ? '·' : '›',
    };
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            formatTime(entry.time),
            style: _terminalStyle(theme, color: scheme.outline),
          ),
          const SizedBox(width: 8),
          Text(
            marker,
            style: _terminalStyle(
              theme,
              color: color,
              fontWeight: FontWeight.w700,
            ),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              entry.text,
              style: _terminalStyle(
                theme,
                color: entry.isDiagnostic
                    ? scheme.onSurfaceVariant
                    : scheme.onSurface,
              ),
            ),
          ),
        ],
      ),
    );
  }
}
