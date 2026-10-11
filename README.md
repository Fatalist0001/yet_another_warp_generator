# warpgen — локальный генератор WARP-конфигов для Amnezia

> **Контекст** — подробности в `инструкция-для-opencode.md`.
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

Подходящими считаются endpoint'ы с `loc != RU`, без colo DME/LED/KJA, со
стабильным (не срезанным ТСПУ) туннелем; они сортируются по возрастанию задержки.

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

## Структура пакетов

- `registration/` — неофициальный API Cloudflare, регистрация WARP, локальное хранение аккаунта
- `tunnel/` — userspace WireGuard/AmneziaWG-туннель, хендшейк, `cdn-cgi/trace`, замер задержки, платформенный bind
- `scanner/` — кандидаты (диапазоны × порты), предфильтр по хендшейку, фильтр `loc != RU` без DME/LED/KJA, ранжирование
- `dnscheck/` — проверка DNS через туннель (пресеты + произвольный IP)
- `config/` — генератор `.conf` (WARP-safe AWG 2.0) и генераторы I1 (QUIC/DNS/SIP/STUN)
- `cli/` — CLI-интерфейс (`register`, `scan`, `dns-check`, `gen`)

## WARP-safe инварианты конфига

Зафиксированы в `config` и проверяются тестами (эталоны — в `refconf/`):

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

Платформенный код изолирован build tags: `tunnel/bind_linux.go`, `tunnel/bind_other.go`.
Никаких linux-only syscall'ов вне этих файлов; root не нужен (кроме `--interface`).

## Ограничения

- **VPN на устройстве:** если активен другой VPN, скан пойдёт через него — проверять при выключенном VPN.
- **ICMP:** не полагаемся на ICMP-пинг — задержку измеряем внутри туннеля.
- **Регистрация:** при недоступном напрямую API Cloudflare регистрация идёт через туннель,
  но для этого нужен уже существующий аккаунт (`--account`).
- **Перебор:** полный диапазон (`188.114.96.0/22` и др.) — это тысячи пар IP×порт;
  за прогон проверяется `--max` кандидатов, скан останавливается после `--target` находок.

## Фичи вне v1

- Split-tunneling
- Кастомный клиент AmneziaWG
- GUI / кроссплатформенная программа
- Релеи, warp-in-warp, MASQUE

## Лицензия

MIT — см. `LICENSE`.
