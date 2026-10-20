/// Раздел «Аккаунт»: регистрация WARP и путь к файлу с ключами.
library;

import 'package:flutter/material.dart';

import '../controllers/account_controller.dart';
import '../services/warpgen_cli.dart';
import '../widgets/ui.dart';

/// Раздел «Аккаунт».
class AccountPage extends StatelessWidget {
  const AccountPage({
    super.key,
    required this.account,
    this.actions = const [],
  });

  final AccountController account;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: account,
      builder: (context, _) {
        final info = account.account;
        return CustomScrollView(
          slivers: [
            SliverAppBar.large(title: const Text('Аккаунт'), actions: actions),
            SliverPadding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 32),
              sliver: SliverList.list(
                children: [
                  if (account.busy && !account.loaded)
                    const Card(
                      child: Padding(
                        padding: EdgeInsets.all(24),
                        child: Center(child: CircularProgressIndicator()),
                      ),
                    )
                  else if (info == null)
                    EmptyState(
                      icon: Icons.person_off_outlined,
                      title: 'Аккаунт WARP не найден',
                      message:
                          'Аккаунт — это ключи туннеля. Он регистрируется один раз '
                          'и хранится локально; без него не работают ни туннель, ни конфиги.',
                      action: FilledButton.icon(
                        onPressed:
                            account.busy ? null : () => account.register(),
                        icon: account.busy
                            ? const SizedBox(
                                width: 18,
                                height: 18,
                                child: CircularProgressIndicator(
                                  strokeWidth: 2,
                                ),
                              )
                            : const Icon(Icons.person_add_alt),
                        label: const Text('Зарегистрировать'),
                      ),
                    )
                  else
                    _AccountCard(account: account, info: info),
                  if (account.error != null) ...[
                    const SizedBox(height: 16),
                    ErrorCard(
                      message: account.error!,
                      onRetry: account.busy ? null : () => account.refresh(),
                    ),
                  ],
                  const SizedBox(height: 16),
                  _FileCard(account: account),
                ],
              ),
            ),
          ],
        );
      },
    );
  }
}

class _AccountCard extends StatelessWidget {
  const _AccountCard({required this.account, required this.info});

  final AccountController account;
  final AccountInfo info;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;

    return SectionCard(
      title: 'Зарегистрированный аккаунт',
      icon: Icons.verified_user_outlined,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          _InfoRow(
            label: 'IPv4',
            value: info.ipv4,
            onCopy: () =>
                copyToClipboard(context, info.ipv4, message: 'IPv4 скопирован'),
          ),
          Divider(height: 20, color: scheme.outlineVariant),
          _InfoRow(
            label: 'IPv6',
            value: info.ipv6,
            onCopy: () =>
                copyToClipboard(context, info.ipv6, message: 'IPv6 скопирован'),
          ),
          Divider(height: 20, color: scheme.outlineVariant),
          _InfoRow(
            label: 'Peer',
            value: info.peerPublicKey,
            onCopy: () => copyToClipboard(
              context,
              info.peerPublicKey,
              message: 'Публичный ключ скопирован',
            ),
          ),
          if (info.id.isNotEmpty) ...[
            Divider(height: 20, color: scheme.outlineVariant),
            _InfoRow(label: 'ID', value: info.shortId),
          ],
          const SizedBox(height: 20),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              OutlinedButton.icon(
                onPressed: account.busy ? null : () => account.refresh(),
                icon: const Icon(Icons.refresh),
                label: const Text('Обновить'),
              ),
              FilledButton.tonalIcon(
                onPressed:
                    account.busy ? null : () => _confirmReregister(context),
                icon: const Icon(Icons.autorenew),
                label: const Text('Новый аккаунт'),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Future<void> _confirmReregister(BuildContext context) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Зарегистрировать новый аккаунт?'),
        content: const Text(
          'Текущий аккаунт будет заменён. Старые конфиги и identity warp-plus '
          'перестанут работать, пока вы не сгенерируете новые.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Перерегистрировать'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    await account.register(fresh: true);
  }
}

class _InfoRow extends StatelessWidget {
  const _InfoRow({required this.label, required this.value, this.onCopy});

  final String label;
  final String value;
  final VoidCallback? onCopy;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Row(
      crossAxisAlignment: CrossAxisAlignment.center,
      children: [
        SizedBox(
          width: 64,
          child: Text(
            label,
            style: theme.textTheme.labelMedium?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ),
        Expanded(
          child: SelectableText(value, style: theme.textTheme.bodyMedium),
        ),
        if (onCopy != null)
          IconButton(
            tooltip: 'Скопировать',
            visualDensity: VisualDensity.compact,
            onPressed: onCopy,
            icon: const Icon(Icons.copy_rounded, size: 18),
          ),
      ],
    );
  }
}

class _FileCard extends StatelessWidget {
  const _FileCard({required this.account});

  final AccountController account;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    return SectionCard(
      title: 'Файл аккаунта',
      icon: Icons.folder_outlined,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: SelectableText(
                  account.accountFile,
                  style: theme.textTheme.bodySmall,
                ),
              ),
              IconButton(
                tooltip: 'Скопировать путь',
                visualDensity: VisualDensity.compact,
                onPressed: () => copyToClipboard(
                  context,
                  account.accountFile,
                  message: 'Путь скопирован',
                ),
                icon: const Icon(Icons.copy_rounded, size: 18),
              ),
            ],
          ),
          const SizedBox(height: 8),
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(Icons.lock_outline, size: 16, color: scheme.error),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  'Файл содержит приватный ключ туннеля. Не публикуйте его и не '
                  'добавляйте в git.',
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: scheme.error,
                  ),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
