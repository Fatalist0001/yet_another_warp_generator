/// Компактная проверка доступности сервисов через текущий выход.
library;

import 'package:flutter/material.dart';

import '../controllers/services_controller.dart';
import '../models/service_status.dart';

/// Полоса с результатами проверки сервисов.
class ServiceStrip extends StatelessWidget {
  const ServiceStrip({
    super.key,
    required this.services,
    required this.enabled,
    this.onRetry,
  });

  final ServicesController services;
  final bool enabled;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: services,
      builder: (context, _) {
        if (!enabled && services.results.isEmpty && services.error == null) {
          return const SizedBox.shrink();
        }
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        final results = services.results;
        final ok = results.where((result) => result.isOk).length;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Icon(Icons.public, size: 18, color: scheme.primary),
                const SizedBox(width: 8),
                Text(
                  'Сервисы',
                  style: theme.textTheme.labelLarge?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
                const SizedBox(width: 8),
                if (results.isNotEmpty)
                  Text(
                    'доступно $ok из ${results.length}',
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: scheme.onSurfaceVariant,
                    ),
                  ),
                const Spacer(),
                if (enabled)
                  IconButton(
                    tooltip: 'Проверить снова',
                    visualDensity: VisualDensity.compact,
                    onPressed: services.checking ? null : onRetry,
                    icon: const Icon(Icons.refresh, size: 18),
                  ),
              ],
            ),
            if (services.checking) ...[
              const SizedBox(height: 6),
              const LinearProgressIndicator(minHeight: 2),
              const SizedBox(height: 8),
              Text(
                'Проверяем доступность через текущий выход…',
                style: theme.textTheme.bodySmall?.copyWith(
                  color: scheme.onSurfaceVariant,
                ),
              ),
            ] else if (services.error != null) ...[
              const SizedBox(height: 6),
              Text(
                services.error!,
                style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
              ),
            ] else if (results.isNotEmpty) ...[
              const SizedBox(height: 10),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  for (final result in results) _ServiceChip(status: result),
                ],
              ),
            ],
          ],
        );
      },
    );
  }
}

class _ServiceChip extends StatelessWidget {
  const _ServiceChip({required this.status});

  final ServiceStatus status;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final good = status.isOk;
    final background = good
        ? scheme.secondaryContainer
        : scheme.errorContainer.withValues(alpha: 0.55);
    final foreground =
        good ? scheme.onSecondaryContainer : scheme.onErrorContainer;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(999),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(
            good ? Icons.check_circle_outline : Icons.cancel_outlined,
            size: 15,
            color: foreground,
          ),
          const SizedBox(width: 6),
          Text(
            status.label,
            style: theme.textTheme.labelMedium?.copyWith(color: foreground),
          ),
          if (status.rttMs > 0) ...[
            const SizedBox(width: 6),
            Text(
              '${status.rttMs} мс',
              style: theme.textTheme.labelSmall?.copyWith(
                color: foreground.withValues(alpha: 0.75),
              ),
            ),
          ],
        ],
      ),
    );
  }
}
