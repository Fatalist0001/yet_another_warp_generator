/// Раздел «Скан»: поиск рабочих endpoint'ов и генерация `.conf`.
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../controllers/account_controller.dart';
import '../controllers/scan_controller.dart';
import '../models/scan_report.dart';
import '../widgets/ui.dart';

/// Сколько кандидатов проверяем и на скольких находках останавливаемся.
const _scanMaxCandidates = 256;
const _scanTarget = 8;

/// Раздел «Скан».
class ScanPage extends StatelessWidget {
  const ScanPage({
    super.key,
    required this.scan,
    required this.account,
    this.actions = const [],
  });

  final ScanController scan;
  final AccountController account;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([scan, account]),
      builder: (context, _) {
        final report = scan.report;
        return CustomScrollView(
          slivers: [
            SliverAppBar.large(
              title: const Text('Скан endpoint'),
              actions: actions,
            ),
            SliverPadding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 32),
              sliver: SliverList.list(
                children: [
                  _ScanControls(scan: scan, account: account),
                  if (scan.running) ...[
                    const SizedBox(height: 16),
                    _ProgressCard(scan: scan),
                  ],
                  if (scan.error != null) ...[
                    const SizedBox(height: 16),
                    ErrorCard(message: scan.error!),
                  ],
                  if (report != null && !scan.running) ...[
                    const SizedBox(height: 16),
                    _SummaryCard(report: report),
                  ],
                  if (report == null &&
                      !scan.running &&
                      scan.error == null) ...[
                    const SizedBox(height: 16),
                    const EmptyState(
                      icon: Icons.radar_outlined,
                      title: 'Ещё не сканировали',
                      message:
                          'Скан перебирает диапазоны Cloudflare, проверяет хендшейк '
                          "и трафик через туннель, а затем ранжирует endpoint'ы по задержке.",
                    ),
                  ],
                  if (report != null) ...[
                    const SizedBox(height: 16),
                    ..._resultCards(context, report),
                  ],
                ],
              ),
            ),
          ],
        );
      },
    );
  }

  List<Widget> _resultCards(BuildContext context, ScanReport report) {
    final hits = report.ranked;
    if (hits.isEmpty) {
      return [
        EmptyState(
          icon: Icons.search_off,
          title: 'Подходящих endpoint нет',
          message:
              'Проверено ${report.checked} кандидатов. Попробуйте ещё раз: '
              'список портов и диапазоны заданы в CLI, а сеть могла быть занята.',
        ),
      ];
    }
    return [
      for (var index = 0; index < hits.length; index++) ...[
        _EndpointCard(hit: hits[index], position: index + 1, scan: scan),
        if (index != hits.length - 1) const SizedBox(height: 8),
      ],
    ];
  }
}

class _ScanControls extends StatefulWidget {
  const _ScanControls({required this.scan, required this.account});

  final ScanController scan;
  final AccountController account;

  @override
  State<_ScanControls> createState() => _ScanControlsState();
}

class _ScanControlsState extends State<_ScanControls> {
  bool _strictRegion = false;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scan = widget.scan;
    final needsAccount =
        widget.account.loaded && widget.account.account == null;

    return SectionCard(
      title: 'Поиск рабочих endpoint',
      icon: Icons.travel_explore,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'Проверим до $_scanMaxCandidates кандидатов и остановимся на $_scanTarget '
            'подходящих. Задержка измеряется внутри туннеля, ICMP не используется.',
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 16),
          FilledButton.icon(
            onPressed: scan.running
                ? null
                : () => scan.scan(
                      maxCandidates: _scanMaxCandidates,
                      target: _scanTarget,
                      strictRegion: _strictRegion,
                    ),
            icon: scan.running
                ? const SizedBox(
                    width: 18,
                    height: 18,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Icon(Icons.radar),
            label: Text(scan.running ? 'Сканируем…' : 'Сканировать'),
          ),
          const SizedBox(height: 8),
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            value: _strictRegion,
            onChanged: scan.running
                ? null
                : (value) => setState(() => _strictRegion = value),
            title: const Text('Только регион не RU'),
            subtitle: const Text(
              "Из России WARP обычно отдаёт RU — обычный режим такие endpoint'ы учитывает",
            ),
          ),
          if (needsAccount)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Row(
                children: [
                  Icon(
                    Icons.info_outline,
                    size: 16,
                    color: theme.colorScheme.error,
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      'Нет аккаунта WARP: скан зарегистрирует его автоматически при доступе к API',
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.error,
                      ),
                    ),
                  ),
                ],
              ),
            ),
        ],
      ),
    );
  }
}

class _ProgressCard extends StatelessWidget {
  const _ProgressCard({required this.scan});

  final ScanController scan;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text('Прогресс', style: theme.textTheme.titleSmall),
                ),
                Text(
                  scan.total > 0
                      ? '${scan.done} из ${scan.total}'
                      : 'подготовка…',
                  style: theme.textTheme.labelMedium?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            ClipRRect(
              borderRadius: BorderRadius.circular(8),
              child: LinearProgressIndicator(
                value: scan.progress,
                minHeight: 6,
              ),
            ),
            if (scan.diagnostics.isNotEmpty) ...[
              const SizedBox(height: 12),
              Text(
                scan.diagnostics.last,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _SummaryCard extends StatelessWidget {
  const _SummaryCard({required this.report});

  final ScanReport report;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final ok = report.ranked.length;

    return SectionCard(
      title: 'Итоги',
      icon: Icons.insights_outlined,
      child: Wrap(
        spacing: 8,
        runSpacing: 8,
        children: [
          _SummaryChip(
            label: 'Проверено',
            value: '${report.checked} из ${report.candidates}',
            scheme: scheme,
          ),
          _SummaryChip(
            label: 'Подходящих',
            value: '$ok',
            scheme: scheme,
            highlight: ok > 0,
          ),
        ],
      ),
    );
  }
}

class _SummaryChip extends StatelessWidget {
  const _SummaryChip({
    required this.label,
    required this.value,
    required this.scheme,
    this.highlight = false,
  });

  final String label;
  final String value;
  final ColorScheme scheme;
  final bool highlight;

  @override
  Widget build(BuildContext context) {
    final background =
        highlight ? scheme.primaryContainer : scheme.surfaceContainerHighest;
    final foreground =
        highlight ? scheme.onPrimaryContainer : scheme.onSurfaceVariant;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(16),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            label,
            style: TextStyle(
              fontSize: 12,
              color: foreground.withValues(alpha: 0.8),
            ),
          ),
          const SizedBox(width: 8),
          Text(
            value,
            style: TextStyle(
              fontSize: 14,
              fontWeight: FontWeight.w700,
              color: foreground,
            ),
          ),
        ],
      ),
    );
  }
}

class _EndpointCard extends StatelessWidget {
  const _EndpointCard({
    required this.hit,
    required this.position,
    required this.scan,
  });

  final ScanHit hit;
  final int position;
  final ScanController scan;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;

    return Card(
      child: ListTile(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
        leading: CircleAvatar(
          backgroundColor: scheme.secondaryContainer,
          child: Text(
            '$position',
            style: TextStyle(
              color: scheme.onSecondaryContainer,
              fontWeight: FontWeight.w700,
            ),
          ),
        ),
        title: Text(
          hit.endpoint,
          style: theme.textTheme.titleSmall?.copyWith(
            fontWeight: FontWeight.w600,
          ),
        ),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text(
            '${hit.locLabel} · узел ${hit.coloLabel} · ${hit.rttLabel} · потери ${hit.lossLabel}',
            style: theme.textTheme.bodySmall?.copyWith(
              color: scheme.onSurfaceVariant,
            ),
          ),
        ),
        trailing: IconButton.filledTonal(
          tooltip: 'Создать .conf',
          onPressed: () => _generate(context),
          icon: const Icon(Icons.file_download_outlined),
        ),
      ),
    );
  }

  Future<void> _generate(BuildContext context) async {
    final messenger = ScaffoldMessenger.of(context);
    try {
      final path = await scan.generateConfig(hit);
      messenger
        ..hideCurrentSnackBar()
        ..showSnackBar(
          SnackBar(
            content: Text('Конфиг сохранён: $path'),
            action: SnackBarAction(
              label: 'Скопировать путь',
              onPressed: () => Clipboard.setData(ClipboardData(text: path)),
            ),
          ),
        );
    } catch (error) {
      messenger
        ..hideCurrentSnackBar()
        ..showSnackBar(SnackBar(content: Text('$error')));
    }
  }
}
