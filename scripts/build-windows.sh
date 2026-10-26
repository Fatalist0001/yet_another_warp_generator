#!/bin/bash
# Сборка Windows-версии «Товарищ майор».
#
# Скрипт собирает Go-ядро под windows/amd64, кладёт `warpgen.exe`,
# `warp-plus.exe` (нужен режиму WARP + Psiphon) и `wintun.dll` (нужен
# системному туннелю) рядом с Flutter-приложением и упаковывает всё в zip.
#
# Установщика нет: папку `Release` распаковывают куда угодно и запускают
# `warpgen_gui.exe`. Настройки, аккаунт, кэш и журнал маршрутов GUI держит в
# `warpgen-data` рядом с exe, поэтому папку можно переносить целиком.
#
# Требуются Flutter SDK с включённой Windows-поддержкой и Visual Studio Build
# Tools с нагрузкой «Разработка классических приложений на C++» (MSVC, CMake,
# Ninja). Пути можно переопределить: GO, FLUTTER, PUB_CACHE,
# WARP_PLUS_WINDOWS (готовый warp-plus.exe), WINTUN_DLL (готовый wintun.dll).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(sed -n 's/^VERSION ?= //p' "$ROOT/Makefile" | tr -d '\r')"
VERSION="${VERSION:-0.1.0}"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

GO="${GO:-go}"
FLUTTER="${FLUTTER:-$ROOT/.toolchain/flutter/bin/flutter}"
PUB_CACHE="${PUB_CACHE:-$ROOT/.toolchain/pub-cache}"
export PUB_CACHE

die() {
	echo "ошибка: $*" >&2
	exit 1
}

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*) ;;
*) die "Windows-версию собираем на Windows: нужны Flutter Windows и Visual Studio C++" ;;
esac

command -v "$GO" >/dev/null 2>&1 || die "нет Go в PATH ($GO)"
[ -x "$FLUTTER" ] || [ -x "$FLUTTER.bat" ] || die "нет Flutter: $FLUTTER"

echo "== 1. Go-ядро для windows/amd64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
	"$GO" -C "$ROOT" build -tags netgo -trimpath \
	-ldflags "-s -w -X main.Version=$VERSION -X main.Commit=$COMMIT -X main.Date=$DATE" \
	-o "$ROOT/warpgen.exe" ./cmd/warpgen

echo "== 2. warp-plus для windows/amd64"
if [ ! -f "$ROOT/warp-plus.exe" ]; then
	SRC="${WARP_PLUS_WINDOWS:-$ROOT/.toolchain/downloads/warp-plus-windows/warp-plus.exe}"
	if [ ! -f "$SRC" ]; then
		ZIP="$ROOT/.toolchain/downloads/warp-plus_windows-amd64.zip"
		mkdir -p "$(dirname "$ZIP")"
		curl -L --fail --retry 3 -o "$ZIP" \
			"https://github.com/bepass-org/warp-plus/releases/download/v1.2.6/warp-plus_windows-amd64.zip"
		rm -rf "$ROOT/.toolchain/downloads/warp-plus-windows"
		unzip -q -o "$ZIP" -d "$ROOT/.toolchain/downloads/warp-plus-windows"
		SRC="$ROOT/.toolchain/downloads/warp-plus-windows/warp-plus.exe"
	fi
	[ -f "$SRC" ] || die "в архиве warp-plus нет warp-plus.exe"
	cp "$SRC" "$ROOT/warp-plus.exe"
fi

echo "== 3. wintun.dll"
# Wintun лежит в отдельном архиве и загружается при сборке: в git бинарники
# драйвера не кладём. DLL должна быть рядом с warpgen.exe (искать её он умеет
# только в каталоге приложения и System32).
if [ -n "${WINTUN_DLL:-}" ]; then
	WIN_WINTUN_SRC="$WINTUN_DLL"
else
	WINTUN_VER="0.14.1"
	WINTUN_DIR="$ROOT/.toolchain/downloads/wintun-$WINTUN_VER"
	if [ ! -f "$WINTUN_DIR/wintun.dll" ] || [ ! -f "$WINTUN_DIR/LICENSE.txt" ]; then
		WINTUN_ZIP="$ROOT/.toolchain/downloads/wintun-$WINTUN_VER.zip"
		mkdir -p "$(dirname "$WINTUN_ZIP")"
		[ -f "$WINTUN_ZIP" ] || curl -L --fail --retry 3 -o "$WINTUN_ZIP" \
			"https://www.wintun.net/builds/wintun-$WINTUN_VER.zip"
		rm -rf "$WINTUN_DIR"
		mkdir -p "$WINTUN_DIR"
		unzip -q -o "$WINTUN_ZIP" "wintun/bin/amd64/wintun.dll" "wintun/LICENSE.txt" \
			-d "$WINTUN_DIR/raw"
		mv "$WINTUN_DIR/raw/wintun/bin/amd64/wintun.dll" "$WINTUN_DIR/wintun.dll"
		mv "$WINTUN_DIR/raw/wintun/LICENSE.txt" "$WINTUN_DIR/LICENSE.txt"
		rm -rf "$WINTUN_DIR/raw"
	fi
	WIN_WINTUN_SRC="$WINTUN_DIR/wintun.dll"
fi
[ -f "$WIN_WINTUN_SRC" ] || die "нет wintun.dll: $WIN_WINTUN_SRC"
cp "$WIN_WINTUN_SRC" "$ROOT/wintun.dll"

echo "== 4. Flutter-приложение"
cd "$ROOT/gui"
if [ ! -f .dart_tool/package_config.json ]; then
	"$FLUTTER" pub get
fi
"$FLUTTER" build windows --release --no-pub

BUNDLE="$ROOT/gui/build/windows/x64/runner/Release"
[ -f "$BUNDLE/warpgen_gui.exe" ] || die "Flutter не создал $BUNDLE/warpgen_gui.exe"

# GUI ищет warpgen.exe рядом с собой (WarpgenLocator), а warp-plus.exe — рядом
# с warpgen (chain.ResolveWarpPlus). Поэтому оба кладём в бандл.
cp "$ROOT/warpgen.exe" "$ROOT/warp-plus.exe" "$ROOT/wintun.dll" "$BUNDLE/"
# Wintun распространяется под GPLv2: в портативной папке держим его лицензию.
if [ -f "$ROOT/.toolchain/downloads/wintun-0.14.1/LICENSE.txt" ]; then
	cp "$ROOT/.toolchain/downloads/wintun-0.14.1/LICENSE.txt" \
		"$BUNDLE/wintun-LICENSE.txt"
fi

echo "== 5. zip"
OUT="$ROOT/warpgen-$VERSION-windows-x64.zip"
rm -f "$OUT"
if command -v zip >/dev/null 2>&1; then
	(cd "$(dirname "$BUNDLE")" && zip -qr "$OUT" "$(basename "$BUNDLE")")
else
	WIN_BUNDLE="$(cygpath -w "$BUNDLE" 2>/dev/null || printf '%s' "$BUNDLE")"
	WIN_OUT="$(cygpath -w "$OUT" 2>/dev/null || printf '%s' "$OUT")"
	powershell.exe -NoProfile -Command \
		"Compress-Archive -Path '${WIN_BUNDLE}' -DestinationPath '$WIN_OUT' -Force"
fi

[ -f "$OUT" ] || die "не удалось создать $OUT"
echo "готово: $OUT"
