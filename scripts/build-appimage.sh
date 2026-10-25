#!/bin/bash
# Сборка AppImage GUI warpgen.
#
# Собирает переносимый образ из уже готовых артефактов: релизного бандла Flutter
# (gui/build/linux/x64/release/bundle), CLI ./warpgen и ./warp-plus. Сами
# артефакты не собирает — это делает `make gui-appimage`, который вызывает
# `build` и `gui-build` перед скриптом.
#
# Инструменты AppImage-экосистемы (linuxdeploy, appimagetool и GTK-плагин) в git
# не лежат: это сторонние бинарники. Скрипт берёт их из корня репозитория или из
# переменных окружения LINUXDEPLOY, APPIMAGETOOL, GTK_PLUGIN.
set -euo pipefail

ROOT="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)"
VERSION="$(sed -n 's/^VERSION ?= //p' "$ROOT/Makefile")"
VERSION="${VERSION:-0.1.0}"
BUNDLE="$ROOT/gui/build/linux/x64/release/bundle"
WORK="${WARPGEN_APPIMAGE_WORK:-$ROOT/.appimage-build}"
APPDIR="$WORK/AppDir"
OUT="$ROOT/warpgen-$VERSION-x86_64.AppImage"

LINUXDEPLOY="${LINUXDEPLOY:-$ROOT/linuxdeploy.AppImage}"
APPIMAGETOOL="${APPIMAGETOOL:-$ROOT/appimagetool.AppImage}"
GTK_PLUGIN="${GTK_PLUGIN:-$ROOT/linuxdeploy-plugin-gtk.sh}"
PLUGIN_DIR="$WORK/plugins"

die() {
	echo "ошибка: $*" >&2
	exit 1
}

need_file() {
	[ -f "$1" ] || die "нет $1${2:+ — $2}"
}

need_file "$BUNDLE/warpgen_gui" "соберите релиз: make gui-build"
need_file "$ROOT/warpgen" "соберите CLI: make build"
need_file "$ROOT/warp-plus" "положите warp-plus в корень репозитория: он нужен режиму WARP + Psiphon"
need_file "$LINUXDEPLOY" "скачайте с https://github.com/linuxdeploy/linuxdeploy/releases или задайте LINUXDEPLOY"
need_file "$APPIMAGETOOL" "скачайте с https://github.com/AppImage/appimagetool/releases или задайте APPIMAGETOOL"
need_file "$GTK_PLUGIN" "скачайте с https://github.com/linuxdeploy/linuxdeploy-plugin-gtk или задайте GTK_PLUGIN"
command -v magick >/dev/null 2>&1 || die "нужен ImageMagick (magick) для иконки"

rm -rf "$WORK"
mkdir -p "$WORK/scratch" "$APPDIR" "$PLUGIN_DIR"

# linuxdeploy ищет плагины рядом с собой или в PATH; кладём копию во временный
# каталог, чтобы не требовать нужного имени файла в корне репозитория.
cp "$GTK_PLUGIN" "$PLUGIN_DIR/linuxdeploy-plugin-gtk.sh"
chmod +x "$PLUGIN_DIR/linuxdeploy-plugin-gtk.sh"
chmod +x "$LINUXDEPLOY" "$APPIMAGETOOL"

echo "== 1. иконка и .desktop"
magick -size 512x512 xc:'#6750A4' \
	-fill white -font DejaVu-Sans-Bold -pointsize 300 -gravity center -annotate +0-16 'Т' \
	"$WORK/warpgen.png"
cat >"$WORK/warpgen.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Товарищ майор
Comment=WARP через userspace-туннель: подключение и проверка сервисов
Exec=warpgen_gui
Icon=warpgen
Categories=Network;Utility;
Terminal=false
X-AppImage-Version=$VERSION
EOF

echo "== 2. linuxdeploy (+ GTK-плагин)"
cp -a "$BUNDLE" "$WORK/scratch/bundle"
LINUXDEPLOY_OUTPUT_VERSION="$VERSION" \
	LINUXDEPLOY_PLUGIN_GTK_FORCE_GTK3=1 \
	PATH="$PLUGIN_DIR:$PATH" \
	"$LINUXDEPLOY" --appimage-extract-and-run \
	--appdir "$APPDIR" \
	--executable "$WORK/scratch/bundle/warpgen_gui" \
	--desktop-file "$WORK/warpgen.desktop" \
	--icon-file "$WORK/warpgen.png" \
	--plugin gtk >"$WORK/linuxdeploy.log" 2>&1 || {
	tail -40 "$WORK/linuxdeploy.log"
	die "linuxdeploy завершился с ошибкой (лог: $WORK/linuxdeploy.log)"
}
tail -5 "$WORK/linuxdeploy.log"

echo "== 3. раскладка Flutter: warpgen_gui + lib/ + data/ в корне AppDir"
cp -a "$BUNDLE"/. "$APPDIR"/
install -m755 "$ROOT/warpgen" "$APPDIR/usr/bin/warpgen"
# warp-plus нужен режиму WARP + Psiphon: CLI ищет его рядом с warpgen.
install -m755 "$ROOT/warp-plus" "$APPDIR/usr/bin/warp-plus"
# дубликат движка в usr/lib не нужен: рядом с бинарником уже есть lib/
rm -f "$APPDIR/usr/lib/libflutter_linux_gtk.so"
# второй вход в usr/bin не нужен, запуск идёт через AppRun
rm -f "$APPDIR/usr/bin/warpgen_gui" "$APPDIR/AppRun.wrapped"

echo "== 4. свой AppRun"
cat >"$APPDIR/AppRun" <<'EOF'
#! /usr/bin/env bash
# Запуск GUI: Flutter ищет data/ и lib/ рядом с бинарником, поэтому бинарник
# лежит в корне AppDir, а GTK-библиотеки — в usr/lib.
set -e
this_dir="$(dirname "$(readlink -f "$0")")"
export APPDIR="$this_dir"
export LD_LIBRARY_PATH="$APPDIR/usr/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
source "$this_dir/apprun-hooks/linuxdeploy-plugin-gtk.sh"

# linuxdeploy жёстко ставит GDK_BACKEND=x11 («Crash with Wayland backend»).
# Под Wayland-композиторами с xwayland-satellite X11-окна теряют ввод и плодят
# служебные окна 1×1, поэтому предпочитаем нативный Wayland. Вернуть X11
# можно явно: WARPGEN_GDK_BACKEND=x11.
if [ -n "${WARPGEN_GDK_BACKEND:-}" ]; then
	export GDK_BACKEND="$WARPGEN_GDK_BACKEND"
elif [ -n "${WAYLAND_DISPLAY:-}" ]; then
	export GDK_BACKEND=wayland
fi

# Обёртка вокруг CLI: путь к бинарнику и файл аккаунта — writable (AppImage
# смонтирован только для чтения, аккаунт рядом с бинарником не создастся).
export WARPGEN_BIN="$this_dir/usr/bin/warpgen"
export WARPGEN_ACCOUNT="${WARPGEN_ACCOUNT:-$HOME/.warpgen/warpgen-account.json}"

exec "$this_dir/warpgen_gui" "$@"
EOF
chmod +x "$APPDIR/AppRun"

echo "== 5. appimagetool"
rm -f "$OUT"
cd "$WORK"
ARCH=x86_64 VERSION="$VERSION" \
	"$APPIMAGETOOL" --appimage-extract-and-run "$APPDIR" "$OUT" >"$WORK/appimagetool.log" 2>&1 || {
	tail -40 "$WORK/appimagetool.log"
	die "appimagetool завершился с ошибкой (лог: $WORK/appimagetool.log)"
}
tail -5 "$WORK/appimagetool.log"

echo "== 6. результат"
# warp-plus обязателен: без него молча ломается половина интерфейса.
[ -x "$APPDIR/usr/bin/warp-plus" ] || die "в AppDir нет usr/bin/warp-plus"
ls -lh "$OUT"
du -sh "$APPDIR"
