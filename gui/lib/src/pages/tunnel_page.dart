/// Главный экран: состояние туннеля, режим и журнал.
library;

import 'package:flutter/material.dart';

import '../controllers/account_controller.dart';
import '../controllers/tunnel_controller.dart';
import '../widgets/ui.dart';

/// Раздел «Туннель».
class TunnelPage extends StatelessWidget {
  const TunnelPage({
    super.key,
    required this.tunnel,
    required this.account,
    this.actions = const [],
  });

  final TunnelController tunnel;
  final AccountController account;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([tunnel, account]),
      builder: (context, _) {
        return CustomScrollView(
          slivers: [
            SliverAppBar.large(title: const Text('Туннель'), actions: actions),
            SliverPadding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 32),
              sliver: SliverList.list(
                children: [
                  _StatusCard(tunnel: tunnel, account: account),
                  const SizedBox(height: 16),
                  _ModeCard(tunnel: tunnel),
                  const SizedBox(height: 16),
                  _JournalCard(tunnel: tunnel),
                ],
              ),
            ),
          ],
        );
      },
    );
  }
}

class _StatusCard extends StatelessWidget {
  const _StatusCard({required this.tunnel, required this.account});

  final TunnelController tunnel;
  final AccountController account;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final text = theme.textTheme;

    final failed = tunnel.status == TunnelStatus.failed;
    final starting = tunnel.status == TunnelStatus.starting;
    final live = tunnel.status == TunnelStatus.live;
    final stopping = tunnel.status == TunnelStatus.stopping;

    final onCard = failed ? scheme.onErrorContainer : scheme.onPrimaryContainer;
    final needsAccount = account.loaded && account.account == null;
    final bindError = tunnel.validateBind();

    return AnimatedContainer(
      duration: const Duration(milliseconds: 300),
      curve: Curves.easeOutCubic,
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(28),
        gradient: LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: failed
              ? [scheme.errorContainer, scheme.errorContainer]
              : [scheme.primaryContainer, scheme.tertiaryContainer],
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              CircleAvatar(
                radius: 26,
                backgroundColor: onCard.withValues(alpha: 0.12),
                child: Icon(
                  _statusIcon(tunnel.status),
                  color: onCard,
                  size: 26,
                ),
              ),
              const SizedBox(width: 16),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      tunnel.headline,
                      style: text.titleLarge?.copyWith(
                        color: onCard,
                        fontWeight: FontWeight.w700,
                      ),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      tunnel.detail,
                      style: text.bodyMedium?.copyWith(
                        color: onCard.withValues(alpha: 0.8),
                      ),
                    ),
                  ],
                ),
              ),
              if (live)
                IconButton.filledTonal(
                  tooltip: 'Скопировать прокси',
                  style: IconButton.styleFrom(
                    backgroundColor: onCard.withValues(alpha: 0.12),
                    foregroundColor: onCard,
                  ),
                  onPressed: () => copyToClipboard(
                    context,
                    tunnel.proxyAddress ?? '',
                    message: 'Адрес прокси скопирован',
                  ),
                  icon: const Icon(Icons.copy_rounded),
                ),
            ],
          ),
          if (starting || stopping) ...[
            const SizedBox(height: 20),
            ClipRRect(
              borderRadius: BorderRadius.circular(8),
              child: LinearProgressIndicator(
                minHeight: 6,
                backgroundColor: onCard.withValues(alpha: 0.15),
                color: onCard,
              ),
            ),
          ],
          if (live) ...[
            const SizedBox(height: 20),
            Text(
              tunnel.proxyAddress ?? '',
              style: text.titleMedium?.copyWith(
                color: onCard,
                fontWeight: FontWeight.w600,
                letterSpacing: 0.2,
              ),
            ),
            const SizedBox(height: 14),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                if ((tunnel.country ?? '').isNotEmpty)
                  _Metric(
                    label: 'Страна',
                    value: tunnel.country!,
                    color: onCard,
                  ),
                if ((tunnel.ip ?? '').isNotEmpty)
                  _Metric(label: 'Выход', value: tunnel.ip!, color: onCard),
                if ((tunnel.colo ?? '').isNotEmpty)
                  _Metric(label: 'Узел', value: tunnel.colo!, color: onCard),
                if ((tunnel.rttMs ?? 0) > 0)
                  _Metric(
                    label: 'Задержка',
                    value: '${tunnel.rttMs} мс',
                    color: onCard,
                  ),
                if (tunnel.prepareTime != null)
                  _Metric(
                    label: 'Подготовка',
                    value: '${tunnel.prepareTime!.inSeconds} с',
                    color: onCard,
                  ),
              ],
            ),
          ],
          if (live && tunnel.warpOnly && tunnel.country == 'RU') ...[
            const SizedBox(height: 12),
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(
                  Icons.public,
                  size: 16,
                  color: onCard.withValues(alpha: 0.8),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'Выход RU — это норма: Cloudflare назначает IP по региону клиента. '
                    'Страну меняет только режим WARP + Psiphon.',
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: onCard.withValues(alpha: 0.8),
                    ),
                  ),
                ),
              ],
            ),
          ],
          if (failed && tunnel.failure != null) ...[
            const SizedBox(height: 16),
            Container(
              padding: const EdgeInsets.all(14),
              decoration: BoxDecoration(
                color: onCard.withValues(alpha: 0.08),
                borderRadius: BorderRadius.circular(16),
              ),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(Icons.error_outline, color: onCard, size: 20),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Text(
                      tunnel.failure!,
                      style: text.bodySmall?.copyWith(color: onCard),
                    ),
                  ),
                ],
              ),
            ),
          ],
          if (needsAccount) ...[
            const SizedBox(height: 16),
            Row(
              children: [
                Icon(
                  Icons.info_outline,
                  size: 18,
                  color: onCard.withValues(alpha: 0.9),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'Сначала зарегистрируйте аккаунт WARP в разделе «Аккаунт»',
                    style: text.bodySmall?.copyWith(
                      color: onCard.withValues(alpha: 0.9),
                    ),
                  ),
                ),
              ],
            ),
          ],
          const SizedBox(height: 24),
          Row(
            children: [
              FilledButton.icon(
                onPressed: tunnel.isRunning
                    ? (stopping ? null : () => tunnel.stop())
                    : (needsAccount || bindError != null
                        ? null
                        : () => tunnel.start()),
                style: FilledButton.styleFrom(
                  // onCard поверх градиента давал светлый текст на светлой
                  // кнопке — выглядело как disabled. Явная заливка primary
                  // отличает рабочую кнопку от неактивной.
                  backgroundColor: failed ? scheme.error : scheme.primary,
                  foregroundColor: failed ? scheme.onError : scheme.onPrimary,
                  minimumSize: const Size(0, 52),
                  padding: const EdgeInsets.symmetric(horizontal: 24),
                  textStyle: const TextStyle(
                    fontSize: 16,
                    fontWeight: FontWeight.w600,
                  ),
                ),
                icon: Icon(
                  tunnel.isRunning
                      ? Icons.stop_rounded
                      : Icons.play_arrow_rounded,
                ),
                label: Text(tunnel.isRunning ? 'Отключиться' : 'Подключиться'),
              ),
            ],
          ),
        ],
      ),
    );
  }

  static IconData _statusIcon(TunnelStatus status) {
    switch (status) {
      case TunnelStatus.idle:
        return Icons.shield_outlined;
      case TunnelStatus.starting:
        return Icons.sync_rounded;
      case TunnelStatus.live:
        return Icons.lock_rounded;
      case TunnelStatus.stopping:
        return Icons.hourglass_bottom_rounded;
      case TunnelStatus.failed:
        return Icons.error_outline;
    }
  }
}

class _Metric extends StatelessWidget {
  const _Metric({
    required this.label,
    required this.value,
    required this.color,
  });

  final String label;
  final String value;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.1),
        borderRadius: BorderRadius.circular(16),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(
            label,
            style: TextStyle(
              fontSize: 11,
              color: color.withValues(alpha: 0.75),
              fontWeight: FontWeight.w500,
            ),
          ),
          const SizedBox(height: 2),
          Text(
            value,
            style: TextStyle(
              fontSize: 14,
              color: color,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }
}

class _ModeCard extends StatefulWidget {
  const _ModeCard({required this.tunnel});

  final TunnelController tunnel;

  @override
  State<_ModeCard> createState() => _ModeCardState();
}

class _ModeCardState extends State<_ModeCard> {
  late final TextEditingController _bind = TextEditingController(
    text: widget.tunnel.bind,
  );

  @override
  void dispose() {
    _bind.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final tunnel = widget.tunnel;
    final enabled = !tunnel.isRunning;
    final bindError = tunnel.validateBind();

    return SectionCard(
      title: 'Режим',
      icon: Icons.tune,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            tunnel.warpOnly
                ? 'WARP без Psiphon: быстрее, но выход остаётся российским'
                : 'WARP + Psiphon: меняет страну выхода, но медленнее',
            style: theme.textTheme.bodySmall?.copyWith(
              color: scheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 16),
          SegmentedButton<bool>(
            segments: const [
              ButtonSegment(
                value: true,
                label: Text('WARP'),
                icon: Icon(Icons.shield_outlined),
              ),
              ButtonSegment(
                value: false,
                label: Text('WARP + Psiphon'),
                icon: Icon(Icons.public),
              ),
            ],
            selected: {tunnel.warpOnly},
            onSelectionChanged:
                enabled ? (values) => tunnel.setWarpOnly(values.first) : null,
          ),
          if (!tunnel.warpOnly) ...[
            const SizedBox(height: 16),
            Text('Страны по приоритету', style: theme.textTheme.labelLarge),
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                for (final code in TunnelController.defaultCountries)
                  FilterChip(
                    label: Text(code),
                    selected: tunnel.countries.contains(code),
                    onSelected:
                        enabled ? (_) => tunnel.toggleCountry(code) : null,
                  ),
              ],
            ),
          ],
          const SizedBox(height: 8),
          SwitchListTile.adaptive(
            contentPadding: EdgeInsets.zero,
            value: tunnel.tun,
            onChanged: enabled ? (value) => tunnel.setTun(value) : null,
            title: const Text('Системный туннель (root)'),
            subtitle: Text(
              'Весь трафик машины идёт по цепочке через TUN-устройство и маршруты '
              '(включается режим kill switch). Нужен root — спросим пароль системы. '
              'Посторонний VPN лучше выключить.',
              style: theme.textTheme.bodySmall?.copyWith(
                color: scheme.onSurfaceVariant,
              ),
            ),
          ),
          const SizedBox(height: 20),
          TextField(
            controller: _bind,
            enabled: enabled,
            onChanged: tunnel.setBind,
            decoration: InputDecoration(
              labelText: 'Адрес SOCKS5',
              helperText: 'Только loopback: прокси отдаёт туннель без пароля',
              errorText: bindError,
            ),
          ),
        ],
      ),
    );
  }
}

class _JournalCard extends StatelessWidget {
  const _JournalCard({required this.tunnel});

  final TunnelController tunnel;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final entries = tunnel.journal;

    return SectionCard(
      title: 'Журнал',
      icon: Icons.receipt_long_outlined,
      trailing: Text(
        '${entries.length}',
        style: theme.textTheme.labelMedium?.copyWith(
          color: scheme.onSurfaceVariant,
        ),
      ),
      child: entries.isEmpty
          ? Text(
              'Пока пусто: события появятся после подключения.',
              style: theme.textTheme.bodySmall?.copyWith(
                color: scheme.onSurfaceVariant,
              ),
            )
          : ConstrainedBox(
              constraints: const BoxConstraints(maxHeight: 280),
              child: ListView.separated(
                shrinkWrap: true,
                padding: EdgeInsets.zero,
                itemCount: entries.length,
                separatorBuilder: (_, __) => const SizedBox(height: 10),
                itemBuilder: (context, index) {
                  final entry = entries[entries.length - 1 - index];
                  return Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Padding(
                        padding: const EdgeInsets.only(top: 6),
                        child: Container(
                          width: 8,
                          height: 8,
                          decoration: BoxDecoration(
                            shape: BoxShape.circle,
                            color: severityColor(scheme, entry.severity),
                          ),
                        ),
                      ),
                      const SizedBox(width: 10),
                      SizedBox(
                        width: 62,
                        child: Padding(
                          padding: const EdgeInsets.only(top: 1),
                          child: Text(
                            formatTime(entry.time),
                            style: theme.textTheme.labelSmall?.copyWith(
                              color: scheme.onSurfaceVariant,
                            ),
                          ),
                        ),
                      ),
                      Expanded(
                        child: Text(
                          entry.text,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: entry.isDiagnostic
                                ? scheme.onSurfaceVariant
                                : scheme.onSurface,
                            fontStyle: entry.isDiagnostic
                                ? FontStyle.italic
                                : FontStyle.normal,
                          ),
                        ),
                      ),
                    ],
                  );
                },
              ),
            ),
    );
  }
}
