# warpgen GUI (Flutter, Linux)

Окно в стиле Material You (Material 3) к локальному CLI `warpgen`: туннель,
скан endpoint'ов и аккаунт WARP.

Приложение не вшивает ядро: оно запускает бинарник `warpgen` и читает его
машинный вывод (`run --json`, `scan --json`). Свой сетевой логики в Dart нет —
только запуск команд, разбор событий и состояние интерфейса.

## Запуск

```bash
make build        # из корня репозитория: нужен бинарник ./warpgen
make gui-deps     # flutter pub get
make gui-run      # окно приложения
make gui-test     # офлайн-тесты (фейковые процессы, без сети)
make gui-analyze  # flutter analyze
```

Нужны Flutter SDK (desktop Linux) и GTK3 dev-пакеты (`libgtk-3-dev`,
`ninja-build`, `clang`, `cmake`, `pkg-config`).

Релизная сборка: `gui/build/linux/x64/release/bundle/warpgen_gui` (~24 МБ вместе с
`lib/` и `data/`). В `linux/CMakeLists.txt` добавлена проверка `if(EXISTS …)` вокруг
установки `native_assets`: шаблон Flutter 3.47 падает на чистой сборке приложения без
native assets (`file INSTALL cannot find …/build/native_assets/linux`).

Бинарник ищется в `WARPGEN_BIN`, затем в `PATH`, затем в текущем каталоге; если
не найден, приложение просит путь. Файл аккаунта берётся рядом с бинарником
(или из `WARPGEN_ACCOUNT`), конфиги из GUI сохраняются в
`~/.warpgen/conf/`, identity warp-plus — в `~/.warpgen/chain`.

## Устройство

| Файл | Ответственность |
|---|---|
| `lib/src/platform/` | запуск процессов (`ProcessRunner`) и поиск бинарника |
| `lib/src/services/warpgen_cli.dart` | команды CLI: `run`, `scan`, `gen`, `register` |
| `lib/src/models/` | события пайплайна, отчёт скана, строки журнала |
| `lib/src/controllers/` | состояние туннеля, скана и аккаунта (`ChangeNotifier`) |
| `lib/src/pages/` | разделы «Туннель», «Скан», «Аккаунт», экран настройки |
| `lib/src/app_theme.dart` | тема Material You из одного seed-цвета |

Тесты не запускают процессы: `test/support/fakes.dart` подменяет раннер, а
события подаются строками NDJSON — ровно в том виде, в каком их печатает Go.
