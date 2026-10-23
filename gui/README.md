# warpgen GUI (Flutter, Linux)

Окно в стиле Material You (Material 3) к локальному CLI `warpgen`: туннель,
скан endpoint'ов и аккаунт WARP.

Приложение не вшивает ядро в себя: оно запускает тот же бинарник `warpgen` и читает
его машинный вывод (`run --json`, `scan --json`). Свой сетевой логики в Dart нет —
только запуск команд, разбор событий и состояние интерфейса.

## Запуск

```bash
make build        # из корня репозитория: нужен бинарник ./warpgen
make gui-deps     # flutter pub get
make gui-run      # окно приложения
make gui-test     # все тесты, включая live-e2e (сеть + настоящий ./warpgen)
make gui-analyze  # flutter analyze
make gui-appimage # переносимый AppImage (нужен ./warp-plus в корне)
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

## Режимы подключения

| Режим | Что делает | IP машины |
|---|---|---|
| WARP без Psiphon (по умолчанию) | локальный SOCKS5 на `127.0.0.1:8086` поверх WARP-туннеля | **не меняется** — нужен настроенный прокси в приложении |
| WARP + Psiphon | локальный SOCKS5 с выходом через выбранную страну | **не меняется** — нужен прокси |
| WARP + системный туннель (`--tun`) | TUN-устройство `warpgen0`, маршруты, kill switch, DNS через цепочку | **меняется** — весь трафик машины идёт по цепочке |

### Системный туннель (root)

Переключатель **«Системный туннель (root)»** в разделе «Режим» поднимает системный
TUN через `warpgen run --tun`. Это режим kill switch: весь трафик машины идёт по
цепочке, обрыв цепочки рвёт соединения (это и есть защита). Включается только через
`pkexec` — приложение запросит пароль root у polkit.

Особенности:
- интерфейс `warpgen0` с MTU 1280 (как у WARP-safe-конфига);
- DNS через цепочку (`dns_selected` → 1.1.1.1);
- `/32` внешнего endpoint добавляется в исключения, чтобы UDP-сокет не уходил в свой же TUN;
- при остановке (`SIGINT`) маршруты снимаются автоматически (`routes_reverted`);
- если процесс был убит без остановки (`kill -9`), системные маршруты остаются применёнными до ручного отката: `warpgen tun-revert --dry-run` покажет команды, без `--dry-run` — применит.

Если другой VPN уже активен, системный туннель не запускайте — маршруты конфликтуют.

## AppImage

`make gui-appimage` собирает `warpgen-<версия>-x86_64.AppImage` скриптом
`scripts/build-appimage.sh`: бандл Flutter + `./warpgen` + `./warp-plus`
(без него режим WARP + Psiphon не запустится). Сторонние инструменты
(`linuxdeploy.AppImage`, `appimagetool.AppImage`, `linuxdeploy-plugin-gtk.sh`)
в git не лежат — скрипт проверяет их наличие и подсказывает, что скачать.
Рабочий каталог — `.appimage-build/` (в `.gitignore`).

`AppRun` внутри образа выставляет `WARPGEN_BIN=$APPDIR/usr/bin/warpgen` и
`WARPGEN_ACCOUNT=${WARPGEN_ACCOUNT:-$HOME/.warpgen/warpgen-account.json}`:
AppImage смонтирован только для чтения, поэтому аккаунт и конфиги живут в `$HOME`.
Образ рассчитан на x86_64, требует glibc ≥ 2.34 и FUSE
(без FUSE — `--appimage-extract-and-run`).

Бинарник ищется в `WARPGEN_BIN`, затем в `PATH`, затем в текущем каталоге; если
не найден, GUI просит путь и больше ничего не делает.

## Устройство

| Файл | Ответственность |
|---|---|
| `lib/src/platform/` | запуск процессов (`ProcessRunner`) и поиск бинарника |
| `lib/src/services/warpgen_cli.dart` | команды CLI: `run`, `scan`, `gen`, `register` |
| `lib/src/models/` | события пайплайна, отчёт скана, строки журнала |
| `lib/src/controllers/` | состояние туннеля, скана и аккаунта (`ChangeNotifier`) |
| `lib/src/pages/` | разделы «Туннель», «Скан», «Аккаунт», экран настройки |
| `lib/src/app_theme.dart` | тема Material You из одного seed-цвета |

## Тесты

`gui/test/` — два слоя.

**Обычные тесты** (`test/*.dart`) не запускают процессы: `test/support/fakes.dart`
подменяет раннер, а события подаются строками NDJSON — ровно в том виде, в каком их
печатает Go. Это проверки разбора, контроллеров и виджетов на фейках; идут секунды,
сеть не нужна.

**Live-e2e** (`test/live/`) — настоящий `./warpgen`, настоящие процессы и сеть:

| Файл | Что проверяет |
|---|---|
| `live_gen_test.dart` | `gen` с реальным хендшейком: WARP-safe значения (`S1..S4=0`, `H1..H4`, `MTU 1280`, `PersistentKeepalive` в `[Peer]`), ошибка недоступного endpoint, чтение аккаунта |
| `live_scan_test.dart` | `scan --json`: прогресс, отчёт, ранжирование рабочих endpoint'ов |
| `live_run_test.dart` | `run` до `ready` в режимах WARP и WARP + Psiphon (страна NL), SOCKS5 действительно проводит TCP, остановка по SIGINT |
| `live_ui_test.dart` | интерфейс без скриншотов: нажатие «Подключиться» поднимает настоящий туннель, карточка показывает прокси и страну, «Отключиться» возвращает в исходное состояние |
| `live_register_test.dart` | регистрация через туннель по существующему аккаунту (прямой API Cloudflare из России недоступен) |

Требования live-тестов: собранный `./warpgen` (`make gui-test` собирает его сам)
и сеть. Аккаунт берётся из `WARPGEN_ACCOUNT`, затем `./warpgen-account.json`; если
его нет, suite один раз регистрирует одноразовый аккаунт. Всё пишется во временный
каталог `$TMPDIR/warpgen-gui-live`, репозиторный аккаунт не меняется.

Только обычные тесты, без сети: `cd gui && flutter test test/*.dart`.

`live_ui_test.dart` использует `LiveTestWidgetsFlutterBinding`: с обычным
(фейковые часы) `Process.start`, запущенный из обработчика кнопки, никогда не
завершается, и настоящий CLI в интерфейсе не проверить.
