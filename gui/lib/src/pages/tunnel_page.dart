/// Главный экран: одна кнопка подключения, режим, TUN и терминал состояния.
library;

import 'dart:io';

import 'package:flutter/material.dart';

import '../controllers/services_controller.dart';
import '../controllers/tunnel_controller.dart';
import '../widgets/service_strip.dart';
import '../widgets/terminal_panel.dart';
import '../widgets/ui.dart';

/// Раздел «Подключение».
class TunnelPage extends StatelessWidget {
  const TunnelPage({
    super.key,
    required this.tunnel,
    required this.services,
  });

  final TunnelController tunnel;
  final ServicesController services;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: tunnel,
      builder: (context, _) {
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        final status = tunnel.status;
        final starting = status == TunnelStatus.starting;
        final stopping = status == TunnelStatus.stopping;
        return SafeArea(
          child: SingleChildScrollView(
            padding: const EdgeInsets.fromLTRB(20, 24, 20, 32),
            child: Center(
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 680),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    _StatusHeader(tunnel: tunnel),
                    const SizedBox(height: 24),
                    SizedBox(
                      height: 56,
                      child: FilledButton.icon(
                        onPressed: stopping ? null : tunnel.toggle,
                        icon: starting
                            ? const SizedBox(
                                width: 20,
                                height: 20,
                                child:
                                    CircularProgressIndicator(strokeWidth: 2),
                              )
                            : Icon(
                                tunnel.isRunning
                                    ? Icons.stop_circle_outlined
                                    : Icons.power_settings_new,
                              ),
                        label: Text(
                          stopping
                              ? 'Останавливаем…'
                              : tunnel.isRunning
                                  ? 'Отключиться'
                                  : 'Подключиться',
                        ),
                      ),
                    ),
                    const SizedBox(height: 24),
                    SegmentedButton<bool>(
                      showSelectedIcon: false,
                      expandedInsets: EdgeInsets.zero,
                      segments: const [
                        ButtonSegment(
                          value: true,
                          icon: Icon(Icons.bolt_outlined, size: 18),
                          label: Text('WARP'),
                        ),
                        ButtonSegment(
                          value: false,
                          icon: Icon(Icons.public, size: 18),
                          label: Text('WARP + Psiphon'),
                        ),
                      ],
                      selected: {tunnel.warpOnly},
                      onSelectionChanged: tunnel.isRunning
                          ? null
                          : (values) => tunnel.setWarpOnly(values.first),
                    ),
                    const SizedBox(height: 8),
                    AnimatedSwitcher(
                      duration: const Duration(milliseconds: 180),
                      child: Text(
                        tunnel.warpOnly
                            ? 'Быстро и без смены страны: обычный трафик через WARP. '
                                'Выход остаётся российским.'
                            : 'Медленнее, зато выход в другой стране — для сервисов, '
                                'которые блокируют российские IP.',
                        key: ValueKey(tunnel.warpOnly),
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: scheme.onSurfaceVariant,
                        ),
                      ),
                    ),
                    if (Platform.isLinux) ...[
                      const SizedBox(height: 8),
                      SwitchListTile(
                        contentPadding: EdgeInsets.zero,
                        dense: true,
                        title: const Text('Системный туннель'),
                        subtitle: const Text(
                          'Весь трафик машины через цепочку · нужен root',
                        ),
                        value: tunnel.tun,
                        onChanged: tunnel.isRunning ? null : tunnel.setTun,
                      ),
                    ],
                    const SizedBox(height: 20),
                    TerminalPanel(tunnel: tunnel),
                    const SizedBox(height: 16),
                    ServiceStrip(
                      services: services,
                      enabled: tunnel.isLive,
                      onRetry: () {
                        final socks = tunnel.socks;
                        if (socks != null) services.check(socks);
                      },
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }
}

class _StatusHeader extends StatelessWidget {
  const _StatusHeader({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final (icon, color) = switch (tunnel.status) {
      TunnelStatus.live => (Icons.shield, scheme.primary),
      TunnelStatus.starting => (Icons.sync, scheme.tertiary),
      TunnelStatus.stopping => (Icons.hourglass_bottom, scheme.tertiary),
      TunnelStatus.failed => (Icons.error_outline, scheme.error),
      TunnelStatus.idle => (Icons.shield_outlined, scheme.outline),
    };
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: 44,
          height: 44,
          decoration: BoxDecoration(
            color: color.withValues(alpha: 0.12),
            shape: BoxShape.circle,
          ),
          child: Icon(icon, color: color),
        ),
        const SizedBox(width: 14),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                tunnel.headline,
                style: theme.textTheme.titleLarge?.copyWith(
                  fontWeight: FontWeight.w600,
                ),
              ),
              const SizedBox(height: 2),
              Text(
                tunnel.detail,
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: scheme.onSurfaceVariant,
                ),
              ),
              if (tunnel.isLive && tunnel.proxyAddress != null) ...[
                const SizedBox(height: 4),
                Text(
                  tunnel.proxyAddress!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    fontFamily: 'monospace',
                    color: scheme.primary,
                  ),
                ),
              ],
            ],
          ),
        ),
        if (tunnel.isLive && tunnel.proxyAddress != null)
          IconButton(
            tooltip: 'Скопировать адрес SOCKS5',
            onPressed: () => copyToClipboard(
              context,
              tunnel.proxyAddress!,
              message: 'Адрес SOCKS5 скопирован',
            ),
            icon: const Icon(Icons.copy_all_outlined),
          ),
      ],
    );
  }
}
