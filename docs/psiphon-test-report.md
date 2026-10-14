# Отчёт: схема «WARP → Psiphon» (warp-plus, bepass-org) — тест из сети пользователя

Дата прогона: 2026-10-11. Каждый запуск ≤3 мин, после каждого теста процессы убиты.
Код warp-plus не менялся — только сборка/загрузка, запуск и замеры.

## 1. Что запускалось

- **Бинарник:** официальный релиз `warp-plus_linux-amd64.zip` с github.com/bepass-org/warp-plus/releases
  (тег **v1.2.6** — последний на дату теста).
- **Сборка из исходников здесь не работает:** `go build` с тулчейнами go1.26.3 и go1.24.3
  (версия из `go.mod`: `go 1.24.0`, `toolchain go1.24.3`) падает паникой на старте —
  `panic: tls.ConnectionState doesn't match` в `psiphon-tls` (unsafe-сравнение раскладки
  `crypto/tls.ConnectionState`). В CI проекта релизы собираются под **Go 1.22**
  (`.github/workflows/go-build.yaml`), поэтому рабочий бинарник — только релизный.
- **Флаги подтверждены через `--help`:** `-v`, `-4/-6`, `-b/--bind` (default 127.0.0.1:8086),
  `-e/--endpoint`, `-k/--key`, `--dns`, `--gool`, `--cfon`, `--country` (default AT),
  `--scan`, `--rtt`, `--cache-dir`, `--fwmark`, `--reserved`, `--wgconf`, `--test-url`, `-c/--config`.
- **Аккаунт:** первая попытка упёрлась в зависание на регистрации
  (`attempting TLS dial fprint 1` → api.cloudflareclient.com недоступен напрямую из этой сети,
  warp-plus висит без таймаута). Поэтому в `--cache-dir` подставлен готовый `wgcf-identity.json`,
  собранный из `test-account.json` этого проекта (id/token/ключи — только данные, код не менялся;
  это аналог флага `--account` у warpgen). После этого идентичность грузится штатно
  (`successfully loaded warp identity`). В `config.client_id` подставлены нулевые `reserved` (`AAAA`).

## 2. Endpoint'ы (warpgen scan, NODE не DME/LED/KJA)

`./warpgen scan -account test-account.json --max 512 --target 4` — все четыре подходящих: NODE **FRA**, SEEN AS RU, RTT 87–89 ms:

| ENDPOINT | NODE |
|---|---|
| 188.114.98.108:4500 | FRA |
| 188.114.99.80:500 | FRA |
| 188.114.98.248:1701 | FRA |
| 188.114.98.164:2408 | FRA |

Дополнительно (не из скана): `162.159.192.1:2408` (каноничный engage-адрес) и случайный
endpoint, выбранный самим warp-plus (`-4` без `-e`): `188.114.98.197:854`.

## 3. Таблица прогонов

| # | endpoint | NODE | запрос. страна | Psiphon поднялся | ip | страна/организация | время подключения | скорость | звено при сбое |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 188.114.98.108:4500 (контроль, без --cfon) | FRA | — | — | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 2 | 188.114.99.80:500 (контроль) | FRA | — | — | — | — | нет; ошибка через 42 с | — | (а) нет хендшейка WARP |
| 3 | 188.114.98.248:1701 (контроль) | FRA | — | — | — | — | нет; ошибка через 39 с | — | (а) нет хендшейка WARP |
| 4 | 188.114.98.164:2408 (контроль) | FRA | — | — | — | — | нет; ошибка через 42 с | — | (а) нет хендшейка WARP |
| 5 | 188.114.98.197:854 (контроль, -4 без -e, свой выбор) | — | — | — | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 6 | 162.159.192.1:2408 (контроль, engage) | — | — | — | — | — | нет; ошибка через 43 с | — | (а) нет хендшейка WARP |
| 7 | 188.114.98.108:4500 | FRA | NL | нет | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 8 | 188.114.98.108:4500 | FRA | DE | нет | — | — | нет; ошибка через 44 с | — | (а) нет хендшейка WARP |
| 9 | 188.114.98.108:4500 | FRA | FI | нет | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 10 | 188.114.98.108:4500 | FRA | SE | нет | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 11 | 188.114.98.108:4500 | FRA | PL | нет | — | — | нет; ошибка через 39 с | — | (а) нет хендшейка WARP |
| 12 | 188.114.98.108:4500 | FRA | US | нет | — | — | нет; ошибка через 40 с | — | (а) нет хендшейка WARP |
| 13 | 188.114.99.80:500 | FRA | NL | нет | — | — | нет; ошибка через 42 с | — | (а) нет хендшейка WARP |
| 14 | 188.114.98.248:1701 | FRA | NL | нет | — | — | нет; ошибка через 39 с | — | (а) нет хендшейка WARP |

Команда контроля: `warp-plus -4 -e <ep> -b 127.0.0.1:8086 -v`.
Команда основного теста: `warp-plus -4 -e <ep> --cfon --country <CC> -v`.
Замеры через `curl --proxy socks5h://127.0.0.1:8086` (ifconfig.co/json, ipinfo.io/json,
speed.cloudflare.com 10 МБ) — до адресатов не дошли ни в одном прогоне.

## 4. Фрагменты логов (без ключей)

Типовая картина контрольного прогона (identity грузится, туннель поднять не удаётся):

```
level=INFO msg="using warp endpoints" endpoints="[188.114.98.108:4500 188.114.98.108:4500]"
level=INFO msg="running in normal warp mode"
level=INFO msg="successfully loaded warp identity" subsystem=warp/account
level=DEBUG msg="waiting on handshake"
... (около 30 строк "waiting on handshake" по 1 с)
level=ERROR msg="context deadline exceeded"
```

Основной тест (Psiphon вообще не стартует — warp-plus запускает Psiphon только после того,
как WARP-туннель прошёл проверку связности, `usermodeTunTest`):

```
level=INFO msg="psiphon mode enabled" country=NL
level=INFO msg="running in Psiphon (cfon) mode"
level=INFO msg="using warp endpoints" endpoints="[188.114.98.108:4500 188.114.98.108:4500]"
... "waiting on handshake" × ~30
level=ERROR msg="context deadline exceeded"
```

Сборка из исходников (обе попытки, тулчейн из go.mod и системный):

```
panic: tls.ConnectionState doesn't match
github.com/Psiphon-Labs/psiphon-tls.init.0()
    psiphon-tls@v0.0.0-20240824224428-ca6969e315a9/unsafe.go:42
```

Первый запуск без готовой идентичности (почему потребовался wgcf-identity.json):

```
level=INFO msg="failed to load identity" subsystem=warp/account path=.../primary
level=INFO msg="creating new identity" subsystem=warp/account
level=INFO msg="attempting TLS dial fprint 1" subsystem=warp/account
... (процесс висит, прокси не поднимается)
```

## 5. Выводы

1. **Схема «устройство → WARP → Psiphon» из этой сети не заработала ни в одном прогоне:**
   сбой на первом звене — `(а) нет хендшейка WARP`. Psiphon не запускается в принципе —
   warp-plus стартует его только после готовности WARP-туннеля, поэтому ни IP, ни страны,
   ни скорости замерить не удалось.
2. **Что мешает:** warp-plus v1.2.6 использует чистый WireGuard (stock wireguard-go, без
   обфускации), а этот участок из данной сети режется: хендшейк не прошёл на 6 разных
   endpoint'ах (порты 4500/500/1701/2408/854), включая каноничный `162.159.192.1:2408`
   и endpoint, выбранный самим warp-plus. Это согласуется с прежним замером проекта:
   warpscout с чистым WG — 0/28 рабочих, с AmneziaWG (junk+I1) — 28/28.
3. **Какие страны проходят:** не определить — ни одна из шести (NL, DE, FI, SE, PL, US)
   не дошла до Psiphon; ответ зависит только от участка WARP, который одинаково мёртв
   для всех стран (страна в `--country` влияет на Psiphon-этап, до которого дело не дошло).
4. **Побочные ограничения на этом стенде:** прямая регистрация аккаунта
   (api.cloudflareclient.com) недоступна — warp-plus зависает на TLS-dial без таймаута,
   пришлось подставить готовую идентичность из `test-account.json`; сборка из исходников
   под Go 1.24/1.26 падает паникой psiphon-tls — работает только релизный бинарник (CI: Go 1.22).
5. **Итог:** тестируемый участок (устройство → WARP) из этой сети не поднимается plain-WG,
   поэтому проверить, какую страну видят внешние сервисы через «WARP → Psiphon», здесь
   невозможно; сама цепочка Psiphon-поверх-WARP в прогоне не достигалась и не подтверждена.
