/// Экран первого запуска: GUI не нашёл бинарник `warpgen`.
library;

import 'package:flutter/material.dart';

import '../widgets/ui.dart';

/// Просит указать путь к CLI, если автоматический поиск не сработал.
class SetupPage extends StatefulWidget {
  const SetupPage({
    super.key,
    required this.onLocate,
    this.initialPath = '',
  });

  /// Пытается найти бинарник по введённому пути. `false` — не нашли.
  final Future<bool> Function(String path) onLocate;

  final String initialPath;

  @override
  State<SetupPage> createState() => _SetupPageState();
}

class _SetupPageState extends State<SetupPage> {
  late final TextEditingController _controller = TextEditingController(
    text: widget.initialPath,
  );
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    final path = _controller.text.trim();
    if (path.isEmpty) {
      setState(() => _error = 'Укажите путь к бинарнику warpgen');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    final found = await widget.onLocate(path);
    if (!mounted) return;
    setState(() {
      _busy = false;
      if (!found) _error = 'По пути $path ничего не нашли';
    });
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('warpgen')),
      body: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 560),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                EmptyState(
                  icon: Icons.terminal_rounded,
                  title: 'Не нашли warpgen',
                  message: 'GUI запускает тот же CLI, что и терминал. '
                      'Соберите бинарник командой make build в каталоге проекта, '
                      'добавьте его в PATH или укажите путь ниже.',
                ),
                const SizedBox(height: 16),
                SectionCard(
                  title: 'Путь к бинарнику',
                  icon: Icons.folder_open_outlined,
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      TextField(
                        controller: _controller,
                        enabled: !_busy,
                        autofocus: true,
                        onSubmitted: (_) => _submit(),
                        decoration: const InputDecoration(
                          labelText: 'warpgen',
                          hintText: '/home/user/warpgen/warpgen',
                        ),
                      ),
                      const SizedBox(height: 12),
                      FilledButton.icon(
                        onPressed: _busy ? null : _submit,
                        icon: _busy
                            ? const SizedBox(
                                width: 18,
                                height: 18,
                                child: CircularProgressIndicator(
                                  strokeWidth: 2,
                                ),
                              )
                            : const Icon(Icons.search),
                        label: const Text('Найти и запустить'),
                      ),
                    ],
                  ),
                ),
                if (_error != null) ...[
                  const SizedBox(height: 16),
                  ErrorCard(message: _error!),
                ],
                const SizedBox(height: 24),
                Text(
                  'Можно также задать переменные окружения WARPGEN_BIN (путь к бинарнику) '
                  'и WARPGEN_ACCOUNT (файл аккаунта) и перезапустить приложение.',
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
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
