#!/bin/bash
# Сборка Android APK «Товарищ майор».
#
# Скрипт собирает Go-ядро под android/arm64, кладёт его и warp-plus в
# jniLibs (они не хранятся в git) и вызывает Flutter. Эмулятор не нужен:
# готовый APK ставится на телефон по USB через `adb install -r`.
#
# Требуется Android SDK (platforms;android-36, build-tools;36.0.0,
# platform-tools) и JDK 21. Пути можно переопределить:
#   ANDROID_SDK_ROOT, JAVA_HOME, FLUTTER, PUB_CACHE, GRADLE_USER_HOME.
set -euo pipefail

ROOT="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)"
VERSION="$(sed -n 's/^VERSION ?= //p' "$ROOT/Makefile")"
VERSION="${VERSION:-0.1.0}"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

SDK="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-$ROOT/.toolchain/android-sdk}}"
JDK="${JAVA_HOME:-$ROOT/.toolchain/jdk21}"
FLUTTER="${FLUTTER:-$ROOT/.toolchain/flutter/bin/flutter}"
PUB_CACHE="${PUB_CACHE:-$ROOT/.toolchain/pub-cache}"
GRADLE_USER_HOME="${GRADLE_USER_HOME:-$ROOT/.toolchain/gradle-home}"
TMPDIR="${TMPDIR:-$ROOT/.toolchain/tmp}"
export ANDROID_SDK_ROOT="$SDK"
export ANDROID_HOME="$SDK"
export JAVA_HOME="$JDK"
export PUB_CACHE GRADLE_USER_HOME TMPDIR
export PATH="$JDK/bin:$SDK/platform-tools:$PATH"
mkdir -p "$TMPDIR" "$GRADLE_USER_HOME/init.d"
# В этом окружении dl.google.com недоступен; dl-ssl.google.com — тот же Maven.
cp "$ROOT/scripts/gradle-google-mirror.init.gradle" \
	"$GRADLE_USER_HOME/init.d/google-mirror.gradle"

JNI="$ROOT/gui/android/app/src/main/jniLibs/arm64-v8a"
mkdir -p "$JNI"

die() {
	echo "ошибка: $*" >&2
	exit 1
}

[ -x "$SDK/platform-tools/adb" ] || die "нет Android SDK: $SDK (нужен platform-tools)"
[ -f "$SDK/platforms/android-36/android.jar" ] ||
	die "нет platforms;android-36 — установите через sdkmanager"
[ -x "$SDK/build-tools/36.0.0/aapt2" ] ||
	die "нет build-tools;36.0.0 — установите через sdkmanager"
[ -x "$FLUTTER" ] || die "нет Flutter: $FLUTTER"

# Flutter 3.47 требует NDK 28.2.13676358 даже без нативной сборки: он включает
# synthetic externalNativeBuild, чтобы AGP скачал NDK. В проекте нативной
# сборки нет (Go и warp-plus — готовые ELF), поэтому при отсутствии NDK делаем
# минимальную заглушку: source.properties + llvm-strip для strip-задачи. Если
# настоящий NDK установлен, он используется как есть.
NDK_VERSION="${NDK_VERSION:-28.2.13676358}"
NDK_DIR="$SDK/ndk/$NDK_VERSION"
if [ ! -f "$NDK_DIR/source.properties" ]; then
	echo "== NDK $NDK_VERSION не найден: минимальная заглушка (нативной сборки нет)"
	mkdir -p "$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64/bin"
	printf 'Pkg.Desc=Android NDK\nPkg.Revision=%s\n' "$NDK_VERSION" >"$NDK_DIR/source.properties"
	STRIP="$(command -v llvm-strip || command -v llvm-strip-21 || command -v llvm-strip-20 || command -v strip || true)"
	if [ -n "$STRIP" ]; then
		ln -sf "$STRIP" "$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip"
	else
		# Go-бинарники уже собраны с -s -w; без strip APK просто чуть больше.
		cat >"$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip" <<'EOF'
#!/bin/sh
exit 0
EOF
		chmod +x "$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip"
	fi
	OBJCOPY="$(command -v llvm-objcopy || command -v llvm-objcopy-21 || command -v llvm-objcopy-20 || true)"
	if [ -n "$OBJCOPY" ]; then
		ln -sf "$OBJCOPY" "$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-objcopy"
	fi
	cat >"$NDK_DIR/ndk-build" <<'EOF'
#!/bin/sh
echo "warpgen: нативная сборка не используется" >&2
exit 1
EOF
	chmod +x "$NDK_DIR/ndk-build"
fi
FLUTTER_SDK="$(cd "$(dirname "$(readlink -f "$FLUTTER")")/.." && pwd)"
cat >"$ROOT/gui/android/local.properties" <<EOF
sdk.dir=$SDK
flutter.sdk=$FLUTTER_SDK
EOF

echo "== 1. Go-ядро для android/arm64"
GOTMPDIR="$TMPDIR" GOOS=android GOARCH=arm64 CGO_ENABLED=0 GOPROXY=off \
	go -C "$ROOT" build -tags netgo -trimpath \
	-ldflags "-s -w -X main.Version=$VERSION -X main.Commit=$COMMIT -X main.Date=$DATE" \
	-o "$JNI/libwarpgen.so" ./cmd/warpgen

echo "== 2. warp-plus для android/arm64"
if [ ! -f "$JNI/libwarp-plus.so" ]; then
	SRC="${WARP_PLUS_ANDROID:-$ROOT/.toolchain/downloads/warp-plus-android/warp-plus}"
	if [ ! -f "$SRC" ]; then
		ZIP="$ROOT/.toolchain/downloads/warp-plus_android-arm64.zip"
		mkdir -p "$(dirname "$ZIP")"
		curl -L --fail --retry 3 -o "$ZIP" \
			"https://github.com/bepass-org/warp-plus/releases/download/v1.2.6/warp-plus_android-arm64.zip"
		rm -rf "$ROOT/.toolchain/downloads/warp-plus-android"
		unzip -q -o "$ZIP" -d "$ROOT/.toolchain/downloads/warp-plus-android"
		SRC="$ROOT/.toolchain/downloads/warp-plus-android/warp-plus"
	fi
	cp "$SRC" "$JNI/libwarp-plus.so"
fi
chmod 755 "$JNI/libwarpgen.so" "$JNI/libwarp-plus.so"

echo "== 3. Flutter APK"
cd "$ROOT/gui"
if [ ! -f .dart_tool/package_config.json ]; then
	"$FLUTTER" pub get --offline
fi
"$FLUTTER" build apk --release --target-platform android-arm64 --no-pub

APK="$ROOT/gui/build/app/outputs/flutter-apk/app-release.apk"
[ -f "$APK" ] || die "Flutter не создал $APK"
OUT="$ROOT/warpgen-$VERSION-arm64.apk"
cp "$APK" "$OUT"
echo "готово: $OUT"
"$SDK/build-tools/36.0.0/aapt2" dump badging "$OUT" 2>/dev/null |
	sed -n '1,3p' || true
