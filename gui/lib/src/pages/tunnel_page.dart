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
    this.showTun,
  });

  final TunnelController tunnel;
  final ServicesController services;

  /// Показывать переключатель системного туннеля.
  ///
  /// По умолчанию — на Linux и Windows: на Android его включает VpnService без
  /// выбора. Параметр нужен тестам, которые гоняют одну и ту же разметку на
  /// любой платформе.
  final bool? showTun;

  @override
  Widget build(BuildContext context) {
    // Страница статична: каждый блок сам слушает только нужное состояние.
    // Иначе событие журнала или смена режима перестраивали весь экран.
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
                _ConnectButton(tunnel: tunnel),
                const SizedBox(height: 24),
                _ModeSelector(tunnel: tunnel),
                if (showTun ?? (Platform.isLinux || Platform.isWindows)) ...[
                  const SizedBox(height: 8),
                  _TunSwitch(tunnel: tunnel),
                ],
                const SizedBox(height: 20),
                TerminalPanel(tunnel: tunnel),
                const SizedBox(height: 16),
                AnimatedBuilder(
                  animation: tunnel,
                  builder: (context, _) => ServiceStrip(
                    services: services,
                    enabled: tunnel.isLive,
                    onRetry: () {
                      final socks = tunnel.socks;
                      if (socks != null) services.check(socks);
                    },
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _StatusHeader extends StatelessWidget {
  const _StatusHeader({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: tunnel,
      builder: (context, _) {
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        final color = switch (tunnel.status) {
          TunnelStatus.live => scheme.primary,
          TunnelStatus.starting => scheme.tertiary,
          TunnelStatus.stopping => scheme.tertiary,
          TunnelStatus.failed => scheme.error,
          TunnelStatus.idle => scheme.onSurface,
        };
        return Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    tunnel.headline,
                    style: theme.textTheme.titleLarge?.copyWith(
                      color: color,
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
                        fontFamily: monoFontFamily,
                        fontWeight: FontWeight.w500,
                        color: scheme.primary,
                      ),
                    ),
                  ],
                ],
              ),
            ),
            if (tunnel.isLive && tunnel.proxyAddress != null)
              TextButton(
                onPressed: () => copyToClipboard(
                  context,
                  tunnel.proxyAddress!,
                  message: 'Адрес SOCKS5 скопирован',
                ),
                child: const Text('Копировать'),
              ),
          ],
        );
      },
    );
  }
}

class _ConnectButton extends StatelessWidget {
  const _ConnectButton({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: tunnel,
      builder: (context, _) {
        final starting = tunnel.status == TunnelStatus.starting;
        final stopping = tunnel.status == TunnelStatus.stopping;
        final label = stopping
            ? 'Останавливаем…'
            : tunnel.isRunning
                ? 'Отключиться'
                : 'Подключиться';
        return SizedBox(
          height: 56,
          child: FilledButton(
            onPressed: stopping ? null : tunnel.toggle,
            child: starting
                ? Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const SizedBox(
                        width: 20,
                        height: 20,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      ),
                      const SizedBox(width: 12),
                      Text(label),
                    ],
                  )
                : Text(label),
          ),
        );
      },
    );
  }
}

class _ModeSelector extends StatelessWidget {
  const _ModeSelector({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: tunnel,
      builder: (context, _) {
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            SegmentedButton<bool>(
              showSelectedIcon: false,
              expandedInsets: EdgeInsets.zero,
              segments: const [
                ButtonSegment(value: true, label: Text('WARP')),
                ButtonSegment(value: false, label: Text('WARP + Psiphon')),
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
          ],
        );
      },
    );
  }
}

class _TunSwitch extends StatelessWidget {
  const _TunSwitch({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: tunnel,
      builder: (context, _) => SwitchListTile(
        contentPadding: EdgeInsets.zero,
        dense: true,
        title: const Text('Системный туннель'),
        subtitle: Text(
          Platform.isWindows
              ? 'Весь трафик машины через цепочку · нужны права администратора'
              : 'Весь трафик машины через цепочку · нужен root',
        ),
        value: tunnel.tun,
        onChanged: tunnel.isRunning ? null : tunnel.setTun,
      ),
    );
  }
}
