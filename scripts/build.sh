#!/usr/bin/env bash
set -euo pipefail

# Repo root from script location — repo can live anywhere, no hardcoded path.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Prefer env vars so CI / other machines can override without editing this file.
NDK="${OPENYOU_NDK:-${ANDROID_NDK_HOME:-$HOME/Android/Sdk/ndk/27.0.12077973}/toolchains/llvm/prebuilt/linux-x86_64/bin}"
ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
JAVA_HOME="${JAVA_HOME:-/usr/lib/jvm/java-17-openjdk-amd64}"

DAEMON="$PROJECT/daemon"
WEB="$DAEMON/internal/server/web/index.html"
USERDATA_SRC="$PROJECT/userdata"
USERDATA_DST="$DAEMON/internal/service/importer/userdata"
APK_DIR="$PROJECT/android/app/build/outputs/apk/debug"
ARM64_SO="$PROJECT/android/app/src/main/jniLibs/arm64-v8a/libagent.so"

CLEAN=false
SKIP_TESTS=false
WITH_X86=false
NEW=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --clean)      CLEAN=true; shift ;;
        --skip-tests) SKIP_TESTS=true; shift ;;
        --x86)        WITH_X86=true; shift ;;
        *)            NEW="$1"; shift ;;
    esac
done

# Version source is state.js, not the built artifact (a stale artifact would
# silently override manual version bumps).
increment() { local v=$1; local IFS=.; read -ra p <<< "$v"; echo "${p[0]}.${p[1]}.$((p[2]+1))"; }
STATE_JS="$PROJECT/web/js/core/state.js"
CUR=$(grep -aoP "APP_VERSION\s*=\s*'\K[^']+" "$STATE_JS")
NEW="${NEW:-$(increment "$CUR")}"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  OPENYOU APK Build  v$CUR → v$NEW  ($(date +%F))"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo "═══ Building web sources..."
python3 "$PROJECT/web/build.py"
echo "   ✅ web build OK"
echo ""

# Keep source and artifact versions in sync.
sed -i "s/APP_VERSION = '.*'/APP_VERSION = '$NEW'/" "$WEB"
sed -i "s/BUILD_DATE = '.*'/BUILD_DATE = '$(date +%F)'/" "$WEB"
sed -i "s/APP_VERSION = '.*'/APP_VERSION = '$NEW'/" "$STATE_JS"
sed -i "s/BUILD_DATE = '.*'/BUILD_DATE = '$(date +%F)'/" "$STATE_JS"
# gradle versionName used to stay frozen at 1.0.0 while the web version moved on (JsBridge reads it).
GRADLE_KTS="$PROJECT/android/app/build.gradle.kts"
if [[ -f "$GRADLE_KTS" ]]; then
    sed -i -E "s/versionName = \"[^\"]*\"/versionName = \"$NEW\"/" "$GRADLE_KTS"
fi

if $CLEAN; then
    echo "═══ [clean] Killing stale gradle daemons..."
    pkill -9 -f gradle 2>/dev/null || true
    rm -rf "$HOME/.gradle/daemon/8.4/"*.lock 2>/dev/null || true
    echo ""
fi

REBUILD_GO=false

if $CLEAN; then
    REBUILD_GO=true
    echo "→ Mode: full rebuild (--clean)"
else
    # Any newer file in daemon/ triggers rebuild — go.mod and //go:embed assets are compiled in.
    if [[ -f "$ARM64_SO" ]]; then
        if find "$DAEMON" -type f -newer "$ARM64_SO" 2>/dev/null | grep -q .; then
            REBUILD_GO=true
        fi
    else
        REBUILD_GO=true
    fi
fi

echo "→ Change detection:"
printf "   Go daemon:    %s\n" "$($REBUILD_GO && echo "🔧 changed → will rebuild" || echo "✅ unchanged → skip")"
echo ""

# Fresh clone / open-source build ships an empty userdata/ — fall back to the example skeleton.
if [[ -d "$USERDATA_SRC" ]] && [[ -n "$(ls -A "$USERDATA_SRC" 2>/dev/null)" ]]; then
    echo "═══ Syncing userdata/..."
    rsync -a --delete --exclude='PLACEHOLDER' "$USERDATA_SRC/" "$USERDATA_DST/"
else
    echo "═══ userdata/ empty or missing → syncing userdata.example/ skeleton..."
    rsync -a --delete --exclude='PLACEHOLDER' "$PROJECT/userdata.example/" "$USERDATA_DST/"
fi
echo "   → daemon/internal/service/importer/userdata/"
echo ""

if $REBUILD_GO && ! $SKIP_TESTS; then
    cd "$DAEMON"

    echo "═══ Running go vet ./..."
    go vet ./...
    echo "   ✅ vet passed"
    echo ""

    echo "═══ Running go test ./..."
    go test ./...
    echo "   ✅ all tests passed"
    echo ""

elif $REBUILD_GO; then
    echo "═══ Go vet + test skipped (--skip-tests)"
    echo ""
else
    echo "═══ Go daemon unchanged, skipping vet + test."
    echo ""
fi

if $REBUILD_GO; then
    cd "$DAEMON"
    # Strip debug_info — saves ~4-5MB from the .so.
    ARM64_STRIP="$NDK/llvm-strip"

    echo "═══ Building libagent.so (arm64)..."
    CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
      CC="$NDK/aarch64-linux-android21-clang" \
      go build -buildmode=c-shared -o libagent_arm64.so ./cmd/daemon/
    echo "   ✅ arm64 OK ($(du -h libagent_arm64.so | cut -f1))"
    if [[ -x "$ARM64_STRIP" ]]; then
        "$ARM64_STRIP" libagent_arm64.so
        echo "   ✂  stripped → ($(du -h libagent_arm64.so | cut -f1))"
    fi
    echo ""

    if $WITH_X86; then
        X86_STRIP="$NDK/llvm-strip"
        echo "═══ Building libagent.so (x86_64)..."
        CGO_ENABLED=1 GOOS=android GOARCH=amd64 \
          CC="$NDK/x86_64-linux-android21-clang" \
          go build -buildmode=c-shared -o libagent.so ./cmd/daemon/
        echo "   ✅ x86_64 OK ($(du -h libagent.so | cut -f1))"
        if [[ -x "$X86_STRIP" ]]; then
            "$X86_STRIP" libagent.so
            echo "   ✂  stripped → ($(du -h libagent.so | cut -f1))"
        fi
        echo ""
    fi

    echo "═══ Copying .so to jniLibs..."
    mkdir -p "$PROJECT/android/app/src/main/jniLibs/arm64-v8a"
    cp libagent_arm64.so "$ARM64_SO"
    echo "   → arm64-v8a/libagent.so"
    if $WITH_X86; then
        mkdir -p "$PROJECT/android/app/src/main/jniLibs/x86_64"
        cp libagent.so "$PROJECT/android/app/src/main/jniLibs/x86_64/libagent.so"
        echo "   → x86_64/libagent.so"
    elif [[ -f "$PROJECT/android/app/src/main/jniLibs/x86_64/libagent.so" ]]; then
        echo "   ⚠  x86_64/libagent.so exists but was NOT rebuilt (no --x86)."
        echo "      Emulator builds will keep running the STALE daemon until"
        echo "      you rebuild with --x86 or delete the x86_64 directory."
    fi
    echo ""
else
    echo "═══ Go daemon unchanged, skipping .so build."
    echo ""
fi

export ANDROID_HOME
export JAVA_HOME

cd "$PROJECT/android"

if $CLEAN; then
    echo "═══ Running gradle clean + assembleDebug (full rebuild)..."
    echo ""
    ./gradlew clean assembleDebug --daemon --build-cache --info
else
    echo "═══ Running gradle assembleDebug (incremental)..."
    echo ""
    ./gradlew assembleDebug --daemon --build-cache --info
fi

echo ""

APK="openyou-v$NEW.apk"
# Remove old APKs first (so wildcard doesn't match the new one)
rm -f "$PROJECT"/openyou-v*.apk 2>/dev/null || true
cp "$APK_DIR/app-debug.apk" "$PROJECT/$APK"

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  ✅ $APK  ($(du -h "$PROJECT/$APK" | cut -f1))"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if command -v adb &>/dev/null && adb devices 2>/dev/null | grep -q 'device$'; then
    adb shell mkdir -p /sdcard/Documents 2>/dev/null
    adb push "$PROJECT/$APK" /sdcard/Documents/$APK 2>/dev/null && \
        echo "  📱 → /sdcard/Documents/$APK"
fi
echo ""
