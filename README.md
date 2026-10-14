# warpgen — локальный генератор WARP-конфигов для Amnezia

> **Ядро** — на базе `github.com/vernette/warpscout` (MIT).

Локальный CLI-инструмент на Go, который регистрирует WARP-аккаунт, сканирует
рабочие endpoint Cloudflare, проверяет DNS через туннель и выдаёт готовый `.conf`
для AmneziaWG с обфускацией. Туннель — userspace: без root, без TUN, без
`VpnService`, без изменения маршрутов системы.

## Требования

- Go ≥ 1.26.3
- Linux/macOS/Windows (разработка и тесты — на Linux)
- Сеть: доступ к API Cloudflare и к диапазонам WARP

## Сборка

```bash
make build          # статический бинарник ./warpgen (CGO_ENABLED=0)
make build-debug    # без -s -w
```

Статический бинарник собирается командой:

```bash
CGO_ENABLED=0 go build -tags netgo -ldflags "-s -w -extldflags '-static'" -o warpgen ./cmd/warpgen
```

## Запуск

```bash
# 1. Зарегистрировать аккаунт WARP (сохраняется в warpgen-account.json)
./warpgen register

# 2. Просканировать endpoint'ы и получить ранжированный список
./warpgen scan --max 512 --target 3

# 3. Проверить DNS через туннель на выбранном endpoint
./warpgen dns-check --endpoint 162.159.192.1:2408

# 4. Сгенерировать конфиг для AmneziaWG
./warpgen gen --endpoint 162.159.192.1:2408 --dns 1.1.1.1 --output warp.conf
```

Все команды принимают `--account` (файл аккаунта), `--fresh` (перерегистрироваться),
`--interface` (привязать UDP-сокет к интерфейсу, linux/root), `--json`, `--debug`.

### Что означают поля вывода `scan`

| Поле | Значение |
|---|---|
| `ENDPOINT` | `ip:port` рабочей точки входа Cloudflare |
| `SEEN AS` | регион, который видит внешний сервис через туннель (`loc` из `cdn-cgi/trace`) |
| `NODE` | узел Cloudflare, на который попал туннель (`colo`) |
| `RTT` | задержка, измеренная **внутри** туннеля (не ICMP) |
| `LOSS` | доля потерь внутри туннеля |
| `STATUS` | `OK` / `нет хендшейка` / `нет данных через туннель` / `регион RU` / `исключённый colo` |

Подходящими считаются endpoint'ы со стабильным (не срезанным ТСПУ) туннелем и без
colo DME/LED/KJA; они сортируются по возрастанию задержки. Регион `RU` больше не
отвергается: из России WARP всегда его отдаёт, поэтому такие endpoint'ы учитываются,
а строка ниже предупреждает об этом. Жёсткий режим — флаг `--strict-region`.

## Chain: AWG → WARP → Psiphon

Один WARP из России не меняет выходной регион (`loc=RU`), но если поверх внешнего
AWG-туннеля запустить **warp-plus** в режиме `--cfon`, он поднимает свой WARP, а
поверх него — Psiphon, и страну меняет именно Psiphon. Команда `chain` автоматизирует
это: проверяет внешний туннель, готовит identity для warp-plus, перебирает страны и
держит SOCKS5, пока процесс жив.

```bash
# 1. Внешний туннель: сгенерировать .conf и подключить его в AmneziaVPN
./warpgen gen --endpoint 162.159.192.1:2408 --output warp.conf

# 2. Запустить цепочку (страна NL, SOCKS5 на 127.0.0.1:8086)
./warpgen chain --countries NL,SE,DE,US,PL

# 3. Пользоваться прокси, пока процесс жив
curl --proxy socks5h://127.0.0.1:8086 https://ifconfig.co/json
```

warp-plus в проект не вшит — вызывается как внешний релизный бинарник v1.2.6
(`--warp-plus PATH`, иначе ищется в `PATH` и рядом с `warpgen`). Identity для него
собирается из аккаунта warpgen в `--cache-dir` (по умолчанию `warpgen-chain/`,
файл `primary/wgcf-identity.json`, права 0600); каталог в `.gitignore`, потому что
содержит приватный ключ.

| Флаг | По умолчанию | Смысл |
|---|---|---|
| `--warp-plus` | поиск в `PATH` и рядом | путь к бинарнику warp-plus |
| `--countries` | `NL,SE,DE,US,PL` | страны через запятую, порядок = приоритет (FI вручную, но он не работает) |
| `--endpoint` | `162.159.192.1:2408` | WARP-endpoint внутреннего туннеля |
| `--bind` | `127.0.0.1:8086` | адрес SOCKS5-прокси |
| `--cache-dir` | `warpgen-chain` | каталог identity warp-plus и кэша Psiphon |
| `--test-url` | `http://example.com/` | URL проверки связи внутри warp-plus (нужен `HEAD=200`, см. ограничения) |
| `--ready-timeout` | `100s` | сколько ждать готовности одной попытки |
| `--attempts` | `2` | попыток на страну |
| `--strict` | `true` | требовать совпадения страны (`--strict=false` — только «не RU») |
| `--account` | `warpgen-account.json` | файл аккаунта warpgen (источник identity) |

Критерий успеха попытки: `cloudflare.com/cdn-cgi/trace` по системному маршруту даёт
`warp=on` и узел не из DME/LED/KJA, а через SOCKS5 `ifconfig.co` и `ipinfo.io`
отвечают валидным JSON с корректным IPv4, при этом ни один не увидел `RU` (и при
`--strict` хотя бы один назвал запрошенную страну). Сбой всех стран — таблица попыток
с причиной (нет хендшейка WARP, гейт warp-plus, Psiphon не построил туннель, не та
страна) и ненулевой код возврата.

### Ручной рецепт тех же шагов

```bash
# 1. Скан → endpoint с NODE не DME/LED/KJA, затем .conf
./warpgen scan --max 512 --target 3
./warpgen gen --endpoint <ip:port> --output warp.conf

# 2. Импортировать .conf в AmneziaVPN и подключить, проверить внешний путь:
curl -s https://www.cloudflare.com/cdn-cgi/trace     # нужно warp=on, colo не DME/LED/KJA

# 3. Запустить warp-plus вручную (релизный бинарник v1.2.6):
warp-plus -4 -e 162.159.192.1:2408 -b 127.0.0.1:8086 --cfon --country NL \
  --test-url http://example.com/ --cache-dir warpgen-chain -v

# 4. Проверить выход через прокси:
curl --proxy socks5h://127.0.0.1:8086 https://ifconfig.co/json
curl --proxy socks5h://127.0.0.1:8086 https://ipinfo.io/json
```

### Известные ограничения chain

- **FI не работает** — Psiphon-этап там стабильно падает (`tunnel establishment timeout`),
  поэтому в список по умолчанию он не входит (хотя запросить можно вручную).
- **Страна не гарантирована:** Psiphon-этап проходит не всегда, а геосервисы могут
  расходиться (был случай: один и тот же IP — PL у `ipinfo.io` и FR у `ifconfig.co`);
  при расхождении печатаются оба ответа.
- **Скорость** через Psiphon — 0.3–0.8 МБ/с (контроль без Psiphon — до 3.45 МБ/с).
- **Нужен внешний туннель** (AmneziaVPN и т.п.) без выхода через российские узлы;
  без него команда завершается с подсказкой.
- **Грабля warp-plus v1.2.6:** своя проверка связи — это HTTP `HEAD` к `--test-url`
  с требованием ровно `200`, а Cloudflare сейчас отдаёт на `HEAD /cdn-cgi/trace` 404.
  Поэтому по умолчанию берётся обходной `--test-url http://example.com/`.
- **Бинарник — только релизный v1.2.6:** сборка из исходников на Go ≥ 1.24 падает
  (`panic: tls.ConnectionState doesn't match`), в CI проекта собирают под Go 1.22.

## Тесты

```bash
make test            # go test ./... -short
make checks          # vet + fmt + test
go vet ./...
gofmt -l .
```

Интеграционный тест (реальная сеть, в CI не гоняется):

```bash
make test-integration
# или
WARPGEN_INTEGRATION=1 go test -tags integration ./cli/ -run Integration -v
```

Интеграционный тест цепочки (нужен внешний туннель и warp-plus в `PATH`):

```bash
WARPGEN_INTEGRATION=1 WARPGEN_CHAIN=1 go test -tags integration ./cli/ -run IntegrationChain -v
```

## Структура пакетов

- `registration/` — неофициальный API Cloudflare, регистрация WARP, локальное хранение аккаунта
- `tunnel/` — userspace WireGuard/AmneziaWG-туннель, хендшейк, `cdn-cgi/trace`, замер задержки, платформенный bind
- `scanner/` — кандидаты (диапазоны × порты), предфильтр по хендшейку, фильтр colo DME/LED/KJA, учёт региона, ранжирование
- `dnscheck/` — проверка DNS через туннель (пресеты + произвольный IP)
- `config/` — генератор `.conf` (WARP-safe AWG 2.0) и генераторы I1 (QUIC/DNS/SIP/STUN)
- `chain/` — цепочка «внешний AWG-туннель → WARP → Psiphon»: запуск внешнего warp-plus и проверка SOCKS5
- `cli/` — CLI-интерфейс (`register`, `scan`, `dns-check`, `gen`, `chain`)

## WARP-safe инварианты конфига

Зафиксированы в `config` и проверяются тестами:

- `S1 S2 S3 S4 = 0`, `H1 H2 H3 H4 = 1 2 3 4` — иначе Cloudflare не поймёт пакеты;
- `MTU = 1280`, `PersistentKeepalive = 25` (в секции `[Peer]`);
- обфускация: junk-пакеты `Jc/Jmin/Jmax` + `I1` (маскировка под QUIC), поле строго заглавное;
- порядок полей `[Interface]`/`[Peer]` — как в эталонных конфигах.

## Платформы

| Платформа | Статус |
|---|---|
| Linux | Разработка и тесты |
| Android | Портирование (Go → нативная библиотека, `-buildmode=c-shared`) |
| Windows | Портирование |

Платформенный код изолирован build tags: `tunnel/bind_linux.go`, `tunnel/bind_other.go`
(и `chain/proc_unix.go`, `chain/proc_windows.go` для управления группой процессов).
Никаких linux-only syscall'ов вне этих файлов; root не нужен (кроме `--interface`).

## Ограничения

- **Выходной регион RU (важно):** из России WARP всегда показывает внешним сервисам
  регион `RU` — Cloudflare назначает выходной IP по геолокации клиента, а не по узлу
  (замер: `loc=RU` у 77 из 77 рабочих endpoint'ов, `ipinfo.io`/`ifconfig.co` также
  видят Россию). Поэтому такие endpoint'ы теперь **учитываются** (вариант «пока
  приемлемо»), а в выводе появляется предупреждение. Цель «сервис не видит российский
  IP» один WARP из РФ **не решает** — для этого нужна цепочка через не-RU узел.
  Туннель при этом работает: ~77% endpoint'ов поднимают хендшейк, узлы FRA/HEL,
  задержка 70–200 мс.
- **VPN на устройстве:** если активен другой VPN, скан пойдёт через него — проверять при выключенном VPN.
- **ICMP:** не полагаемся на ICMP-пинг — задержку измеряем внутри туннеля.
- **Регистрация:** API `api.cloudflareclient.com` из РФ часто недоступен напрямую —
  тогда регистрация идёт через туннель, но для этого нужен уже существующий аккаунт (`--account`).
- **Перебор:** полный диапазон (`188.114.96.0/22` и др.) — это тысячи пар IP×порт;
  за прогон проверяется `--max` кандидатов, скан останавливается после `--target` находок.
  Автоподбор endpoint для `dns-check` (без `--endpoint`) ограничен 40–200 проверками.

## Фичи вне v1

- Split-tunneling
- Кастомный клиент AmneziaWG
- GUI / кроссплатформенная программа
- Релеи, warp-in-warp, MASQUE

## Лицензия

MIT — см. `LICENSE`.
