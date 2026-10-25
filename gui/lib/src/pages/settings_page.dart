/// Настройки: DNS, сервисы, аккаунт и приложение.
library;

import 'package:flutter/material.dart';

import '../app.dart';
import '../controllers/account_controller.dart';
import '../controllers/services_controller.dart';
import '../controllers/settings_controller.dart';
import '../controllers/tunnel_controller.dart';
import '../models/dns_preset.dart';
import '../widgets/ui.dart';

/// Раздел «Настройки».
class SettingsPage extends StatelessWidget {
  const SettingsPage({
    super.key,
    required this.deps,
    required this.tunnel,
    required this.account,
    required this.settings,
    required this.services,
    required this.onRelocate,
  });

  final AppDependencies deps;
  final TunnelController tunnel;
  final AccountController account;
  final SettingsController settings;
  final ServicesController services;
  final Future<bool> Function(String? path) onRelocate;

  @override
  Widget build(BuildContext context) {
    // Каждая карточка слушает только свои контроллеры: события туннеля не
    // должны перестраивать DNS, аккаунт и тему (это заметно на прокрутке).
    return SafeArea(
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(20, 24, 20, 32),
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 760),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                AnimatedBuilder(
                  animation: tunnel,
                  builder: (context, _) => _connectionCard(context),
                ),
                const SizedBox(height: 16),
                AnimatedBuilder(
                  animation: settings,
                  builder: (context, _) => _dnsCard(context),
                ),
                const SizedBox(height: 16),
                AnimatedBuilder(
                  animation: Listenable.merge([settings, services, tunnel]),
                  builder: (context, _) => _servicesCard(context),
                ),
                const SizedBox(height: 16),
                AnimatedBuilder(
                  animation: Listenable.merge([account, tunnel]),
                  builder: (context, _) => _accountCard(context),
                ),
                const SizedBox(height: 16),
                AnimatedBuilder(
                  animation: settings,
                  builder: (context, _) => _appCard(context),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _connectionCard(BuildContext context) {
    final theme = Theme.of(context);
    final choices = <String>{
      ...TunnelController.defaultCountries,
      ...tunnel.countries,
    }.toList();
    return SectionCard(
      title: 'Подключение',
      icon: Icons.tune,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'Режим и системный туннель переключаются на главной. Здесь — '
            'страны Psiphon и локальный порт.',
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 18),
          Text('Страны Psiphon (приоритет)', style: theme.textTheme.labelLarge),
          const SizedBox(height: 8),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              for (final code in choices)
                FilterChip(
                  label: Text(code),
                  selected: tunnel.countries.contains(code),
                  onSelected: tunnel.isRunning
                      ? null
                      : (_) => tunnel.toggleCountry(code),
                ),
            ],
          ),
          const SizedBox(height: 18),
          _BindField(
            value: tunnel.bind,
            enabled: !tunnel.isRunning,
            error: tunnel.validateBind(),
            onChanged: tunnel.setBind,
          ),
        ],
      ),
    );
  }

  Widget _dnsCard(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final selected = settings.selectedPreset;
    final presets = settings.presets;
    return SectionCard(
      title: 'DNS',
      icon: Icons.dns_outlined,
      trailing: settings.loadingPresets
          ? const SizedBox(
              width: 18,
              height: 18,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : IconButton(
              tooltip: 'Обновить список DNS',
              visualDensity: VisualDensity.compact,
              onPressed: settings.refreshPresets,
              icon: const Icon(Icons.refresh, size: 18),
            ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (presets.isEmpty)
            Text(
              settings.error ?? 'Список DNS не загружен',
              style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
            )
          else
            DropdownButtonFormField<String>(
              key: ValueKey(settings.settings.dnsId),
              initialValue: presets.any(
                (preset) => preset.id == settings.settings.dnsId,
              )
                  ? settings.settings.dnsId
                  : null,
              decoration: const InputDecoration(labelText: 'DNS-резолвер'),
              items: [
                for (final preset in presets)
                  DropdownMenuItem(
                    value: preset.id,
                    child: Text(preset.label),
                  ),
              ],
              onChanged: tunnel.isRunning
                  ? null
                  : (value) {
                      if (value != null) settings.setDnsId(value);
                    },
            ),
          if (selected != null) ...[
            const SizedBox(height: 10),
            Text(
              selected.note,
              style: theme.textTheme.bodySmall?.copyWith(
                color: scheme.onSurfaceVariant,
              ),
            ),
            if (selected.kind == DnsKind.custom) ...[
              const SizedBox(height: 10),
              _CustomDnsField(
                value: settings.settings.customDns,
                enabled: !tunnel.isRunning,
                onChanged: settings.setCustomDns,
              ),
            ],
            if (selected.kind == DnsKind.provider) ...[
              const SizedBox(height: 10),
              _providerDns(context),
            ],
            if (selected.kind == DnsKind.group ||
                selected.kind == DnsKind.hostname ||
                selected.kind == DnsKind.ip) ...[
              const SizedBox(height: 10),
              _presetDns(context, selected),
            ],
          ],
          const SizedBox(height: 12),
          Text(
            'DNS применяется в системном туннеле. Поддерживается обычный DNS '
            '(IP или hostname), DoH/DoT — нет.',
            style: theme.textTheme.bodySmall?.copyWith(
              color: scheme.onSurfaceVariant,
            ),
          ),
          if (settings.error != null && presets.isNotEmpty) ...[
            const SizedBox(height: 8),
            Text(
              settings.error!,
              style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
            ),
          ],
        ],
      ),
    );
  }

  Widget _providerDns(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final info = settings.providerDns;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          children: [
            Expanded(
              child: Text(
                info == null
                    ? 'DNS провайдера ещё не определён'
                    : '${info.interfaceName.isEmpty ? 'интерфейс' : info.interfaceName}: '
                        '${info.servers.join(', ')}',
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontFamily: 'monospace',
                ),
              ),
            ),
            TextButton(
              onPressed: settings.refreshProvider,
              child: const Text('Определить'),
            ),
          ],
        ),
        if (info?.warning != null && info!.warning.isNotEmpty)
          Text(
            info.warning,
            style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
          ),
        if (settings.providerError != null)
          Text(
            settings.providerError!,
            style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
          ),
      ],
    );
  }

  Widget _presetDns(BuildContext context, DnsPreset preset) {
    final theme = Theme.of(context);
    final resolved = preset.resolvedIp;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Text(
                resolved.isEmpty ? preset.value : resolved,
                style: theme.textTheme.bodyMedium?.copyWith(
                  fontFamily: 'monospace',
                ),
              ),
            ),
            TextButton(
              onPressed: settings.loadingPresets
                  ? null
                  : () => settings.refreshPresets(resolve: true),
              child: const Text('Проверить IP'),
            ),
          ],
        ),
        if (preset.error.isNotEmpty)
          Text(
            preset.error,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.error,
            ),
          ),
      ],
    );
  }

  Widget _servicesCard(BuildContext context) {
    final theme = Theme.of(context);
    return SectionCard(
      title: 'Сервисы',
      icon: Icons.public,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SwitchListTile(
            contentPadding: EdgeInsets.zero,
            title: const Text('Проверять после подключения'),
            subtitle: const Text(
              'Показывать доступность Яндекса, VK, Госуслуг, Ozon, Wildberries '
              'и заблокированных сервисов',
            ),
            value: settings.settings.checkServices,
            onChanged: settings.setCheckServices,
          ),
          if (tunnel.isLive)
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                onPressed: services.checking
                    ? null
                    : () {
                        final socks = tunnel.socks;
                        if (socks != null) services.check(socks);
                      },
                icon: const Icon(Icons.play_arrow_outlined, size: 18),
                label: const Text('Проверить сейчас'),
              ),
            ),
          if (services.error != null)
            Text(
              services.error!,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.error,
              ),
            ),
        ],
      ),
    );
  }

  Widget _accountCard(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final info = account.account;
    return SectionCard(
      title: 'Аккаунт WARP',
      icon: Icons.person_outline,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (account.busy && !account.loaded)
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 20),
              child: Center(child: CircularProgressIndicator()),
            )
          else if (info == null)
            Text(
              'Аккаунт создаётся автоматически при первом подключении. '
              'Отдельный .conf для импорта не нужен: программа сама '
              'генерирует и использует конфигурацию.',
              style: theme.textTheme.bodyMedium?.copyWith(
                color: scheme.onSurfaceVariant,
              ),
            )
          else ...[
            _InfoRow(label: 'IPv4', value: info.ipv4),
            _InfoRow(label: 'IPv6', value: info.ipv6),
            _InfoRow(label: 'Peer', value: info.peerPublicKey),
            _InfoRow(label: 'ID', value: info.shortId),
            const SizedBox(height: 6),
            Text(
              'Файл: ${account.accountFile}',
              style: theme.textTheme.bodySmall?.copyWith(
                color: scheme.onSurfaceVariant,
                fontFamily: 'monospace',
              ),
            ),
          ],
          if (account.error != null) ...[
            const SizedBox(height: 10),
            Text(
              account.error!,
              style: theme.textTheme.bodySmall?.copyWith(color: scheme.error),
            ),
          ],
          const SizedBox(height: 14),
          Wrap(
            spacing: 10,
            runSpacing: 10,
            children: [
              if (info == null)
                FilledButton.tonalIcon(
                  onPressed: account.busy ? null : account.register,
                  icon: const Icon(Icons.person_add_alt_1, size: 18),
                  label: const Text('Создать сейчас'),
                ),
              OutlinedButton.icon(
                onPressed: account.busy || tunnel.isRunning
                    ? null
                    : () => _confirmRecreate(context),
                icon: const Icon(Icons.restart_alt, size: 18),
                label: const Text('Пересоздать конфигурацию'),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _appCard(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    return SectionCard(
      title: 'Приложение',
      icon: Icons.settings_outlined,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: const Icon(Icons.terminal),
            title: const Text('Бинарник warpgen'),
            subtitle: Text(
              deps.cli.binaryPath,
              style: theme.textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
                color: scheme.onSurfaceVariant,
              ),
            ),
            trailing: TextButton(
              onPressed: () => _showBinaryDialog(context),
              child: const Text('Изменить'),
            ),
          ),
          const SizedBox(height: 8),
          Text('Тема', style: theme.textTheme.labelLarge),
          const SizedBox(height: 8),
          SegmentedButton<String>(
            showSelectedIcon: false,
            expandedInsets: EdgeInsets.zero,
            segments: const [
              ButtonSegment(
                value: 'system',
                icon: Icon(Icons.brightness_auto_outlined, size: 18),
                label: Text('Системная'),
              ),
              ButtonSegment(
                value: 'light',
                icon: Icon(Icons.light_mode_outlined, size: 18),
                label: Text('Светлая'),
              ),
              ButtonSegment(
                value: 'dark',
                icon: Icon(Icons.dark_mode_outlined, size: 18),
                label: Text('Тёмная'),
              ),
            ],
            selected: {settings.settings.themeMode},
            onSelectionChanged: (values) => settings.setThemeMode(values.first),
          ),
          const SizedBox(height: 12),
          Text(
            'Настройки: ${deps.settingsPath}',
            style: theme.textTheme.bodySmall?.copyWith(
              color: scheme.onSurfaceVariant,
              fontFamily: 'monospace',
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _confirmRecreate(BuildContext context) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Пересоздать конфигурацию?'),
        content: const Text(
          'Будет создан новый WARP-аккаунт, а identity warp-plus заменена. '
          'Текущее подключение нужно будет выполнить заново. '
          'Файлы аккаунта не попадают в интерфейс.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('Пересоздать'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    await account.register(fresh: true);
    var cacheError = false;
    if (account.error == null) {
      try {
        await deps.cli.clearEndpointCache();
      } catch (_) {
        cacheError = true;
      }
    }
    if (!context.mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(
          account.error ??
              (cacheError
                  ? 'Конфигурация пересоздана, но кэш endpoint-ов удалить не удалось'
                  : 'Конфигурация пересоздана'),
        ),
      ),
    );
  }

  Future<void> _showBinaryDialog(BuildContext context) async {
    final controller = TextEditingController(text: deps.cli.binaryPath);
    final path = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Бинарник warpgen'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'GUI запускает тот же CLI, что и терминал. Укажите путь к '
              'бинарнику или оставьте поиск по PATH и переменной WARPGEN_BIN.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 16),
            TextField(
              controller: controller,
              autofocus: true,
              decoration: const InputDecoration(
                labelText: 'Путь к warpgen',
                hintText: '/home/user/warpgen/warpgen',
              ),
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(controller.text.trim()),
            child: const Text('Проверить'),
          ),
        ],
      ),
    );
    controller.dispose();
    if (path == null || path.isEmpty) return;
    final found = await onRelocate(path);
    if (!found && context.mounted) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Не нашли бинарник по пути $path')),
      );
    }
  }
}

class _InfoRow extends StatelessWidget {
  const _InfoRow({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 58,
            child: Text(
              label,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ),
          Expanded(
            child: SelectableText(
              value,
              style: theme.textTheme.bodySmall?.copyWith(
                fontFamily: 'monospace',
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _BindField extends StatefulWidget {
  const _BindField({
    required this.value,
    required this.enabled,
    required this.error,
    required this.onChanged,
  });

  final String value;
  final bool enabled;
  final String? error;
  final ValueChanged<String> onChanged;

  @override
  State<_BindField> createState() => _BindFieldState();
}

class _BindFieldState extends State<_BindField> {
  late final TextEditingController _controller =
      TextEditingController(text: widget.value);

  @override
  void didUpdateWidget(covariant _BindField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.value != _controller.text) {
      _controller.text = widget.value;
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return TextField(
      controller: _controller,
      enabled: widget.enabled,
      onChanged: widget.onChanged,
      decoration: InputDecoration(
        labelText: 'Адрес SOCKS5',
        hintText: '127.0.0.1:8086',
        errorText: widget.error,
        helperText: 'Только loopback: 127.0.0.1',
      ),
    );
  }
}

class _CustomDnsField extends StatefulWidget {
  const _CustomDnsField({
    required this.value,
    required this.enabled,
    required this.onChanged,
  });

  final String value;
  final bool enabled;
  final ValueChanged<String> onChanged;

  @override
  State<_CustomDnsField> createState() => _CustomDnsFieldState();
}

class _CustomDnsFieldState extends State<_CustomDnsField> {
  late final TextEditingController _controller =
      TextEditingController(text: widget.value);

  @override
  void didUpdateWidget(covariant _CustomDnsField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.value != _controller.text) {
      _controller.text = widget.value;
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return TextField(
      controller: _controller,
      enabled: widget.enabled,
      onChanged: widget.onChanged,
      decoration: const InputDecoration(
        labelText: 'Свой DNS',
        hintText: '1.1.1.1, 8.8.8.8',
        helperText: 'Через запятую; hostname допустим',
      ),
    );
  }
}
