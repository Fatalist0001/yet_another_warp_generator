# warpgen GUI (Flutter, Linux и Windows)

Окно в стиле Material You (Material 3) к локальному CLI `warpgen`: туннель,
скан endpoint'ов и аккаунт WARP. Один Dart-код на Linux и Windows; отличаются
только runner'ы и способ мягкой остановки CLI.

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

# Windows (только на Windows): портативный zip без установщика
make windows
```

Нужны Flutter SDK (desktop Linux) и GTK3 dev-пакеты (`libgtk-3-dev`,
`ninja-build`, `clang`, `cmake`, `pkg-config`), а для Windows — Flutter SDK с
Windows-поддержкой и Visual Studio Build Tools с C++.

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
- при остановке (`SIGINT`, а для root-процесса — файл-сигнал `--stop-file`) маршруты снимаются автоматически (`routes_reverted`);
- если процесс был убит без остановки (`kill -9`), системные маршруты остаются применёнными до ручного отката: `warpgen tun-revert --dry-run` покажет команды, без `--dry-run` — применит. На Windows новый запуск откатывает журнал прошлого запуска и снимает свой DNS сам, а если журнала нет (папку перенесли) — `warpgen tun-revert --force`.

В AppImage бинарник лежит на FUSE-монтировании, доступном только пользователю
(`user_id=<uid>`, без `allow_other`), а `pkexec` проверяет доступ к программе
**до** запроса пароля. Поэтому для `--tun` GUI копирует `warpgen` (и `warp-plus`,
если он рядом) в `~/.warpgen/elev/` и запускает копию: иначе `pkexec` падает с
`Error accessing …: Permission denied`, и диалог пароля не появляется. Диалог
показывает агент polkit (KDE/GNOME/lxpolkit); если его нет, в карточке состояния
будет подсказка с причиной.

Остановка root-процесса сигналом невозможна (чужой uid), поэтому GUI создаёт файл
`~/.warpgen/run/stop`, а CLI с `--stop-file` замечает его и завершает пайплайн
сам — с откатом маршрутов.

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

## Windows (портативная сборка)

`make windows` (`scripts/build-windows.sh`) собирает Go-ядро под windows/amd64,
кладёт `warpgen.exe`, `warp-plus.exe` и `wintun.dll` рядом с Flutter-приложением
и упаковывает `gui/build/windows/x64/runner/Release` в
`warpgen-<версия>-windows-x64.zip`. Установщика нет: папку можно распаковать
куда угодно и запустить `warpgen_gui.exe`; реестр не трогается. `wintun.dll`
скачивается при сборке и в git не лежит.

`flutter run -d windows` требует терминала от администратора: готовый exe
запускается с манифестом `requireAdministrator`, а обычный `CreateProcess`
повышенный процесс не стартует. `make windows` собирает без прав — они нужны
только при запуске.

Системный туннель работает как на Linux: переключатель «Системный туннель»
(на первом запуске включён) создаёт Wintun-адаптер `warpgen0`, ведёт весь
IPv4-трафик через цепочку, перенастраивает DNS и отбрасывает IPv6. Для этого GUI
запускается с манифестом `requireAdministrator` — Windows покажет UAC при старте,
а дочерний `warpgen.exe` унаследует права. Выключенный переключатель оставляет
локальный SOCKS5 (`127.0.0.1:8086`).

Все данные портативные — в `warpgen-data` рядом с `warpgen_gui.exe`: `gui.json`,
аккаунт, `endpoints.json`, `chain/`, файл остановки. Если каталог только для
чтения, GUI откатывается в `%USERPROFILE%\.warpgen`.

Исключение — журнал отката маршрутов (`tun-revert`): он хранится в
`%LOCALAPPDATA%\warpgen\state`, а не в папке программы. Zip распаковывают в
другое место, папку переносят и удаляют, а применённые маршруты и DNS остаются
в системе: потерянный журнал означал бы, что откатывать нечем. Перед подключением
рукописный CLI сам снимает свой DNS (`198.18.0.0/15`) с интерфейсов и откатывает
журнал прошлого запуска, так что брошенный процесс больше не оставляет машину
без резолва.

Отличия от Linux-версии:

- мягкая остановка — файлом-сигналом `--stop-file`: `dart:io` на Windows не умеет
  доставлять SIGINT дочернему процессу, `Process.kill` сразу убивает CLI;
- CLI ищется как `warpgen.exe` в `WARPGEN_BIN`, `PATH` (разделитель `;`), рядом с
  `warpgen_gui.exe` и в текущем каталоге;
- домашний каталог — `USERPROFILE`; «DNS провайдера» и пресеты DNS работают
  через CLI (`dns-current` реализован и на Windows).

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
