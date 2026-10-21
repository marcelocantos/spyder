#!/usr/bin/env bash
# Build the darwin-arm64 release tarball into dist/.
# Layout matches the retired .github/workflows/release.yml (🎯T135), plus
# bundled stream-player artifacts under libexec/spyder/player/ (🎯T102):
#   spyder-<ver>-darwin-arm64/{
#     bin/spyder, bin/spyder-killusbmuxd,
#     libexec/spyder/ios,
#     libexec/spyder/player/desktop/player,
#     libexec/spyder/player/android/player.apk  (best-effort — see below),
#   }
#
# iOS is deliberately NOT packaged here: a Development/Ad Hoc signed
# Player.app from one team fails ApplicationVerificationFailed on other
# developers' devices (per-developer codesign/provisioning is not
# multi-tenant). Build and codesign player/ios locally, or point
# SPYDER_PLAYER_IOS at a re-signed .app — see tapper/formula_includes.rb
# caveats and launch_player's resolution order
# (path= → SPYDER_PLAYER_* env → libexec install layout → repo-relative
# dev build outputs; internal/mcp/launch_player.go).
set -euo pipefail
source "$(dirname "$0")/release-common.sh"

VERSION="$(require_version "${1:-}")"
TAG="v${VERSION}"
DIST="$ROOT/dist"
STAGE="$DIST/build"
TARBALL_DIR="spyder-${VERSION}-darwin-arm64"
ASSET="${TARBALL_DIR}.tar.gz"

rm -rf "$DIST"
mkdir -p "$STAGE/bin" "$STAGE/libexec/spyder" "$STAGE/libexec/spyder/player"

echo "release-package: building spyder ${TAG} (darwin/arm64, CGO=1)" >&2
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	go build -trimpath -ldflags "-s -w -X main.version=${TAG} -X github.com/marcelocantos/spyder/internal/runs.BuildBranch=$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)" \
	-o "$STAGE/bin/spyder" .

"$ROOT/scripts/codesign-spyder.sh" "$STAGE/bin/spyder"

echo "release-package: building go-ios" >&2
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
	go build -trimpath -ldflags "-s -w" -mod=mod \
	-o "$STAGE/libexec/spyder/ios" github.com/danielpaulus/go-ios

echo "release-package: building spyder-killusbmuxd" >&2
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
	go build -trimpath -ldflags "-s -w" \
	-o "$STAGE/bin/spyder-killusbmuxd" ./cmd/spyder-killusbmuxd

# Desktop stream player (🎯T102): self-contained C++ glass under player/,
# built for arm64 macOS by `make -C player`. Arch-matched to the spyder
# binary above (darwin/arm64 only — see hygiene.yaml's no-Linux floor).
echo "release-package: building desktop player" >&2
"$ROOT/scripts/package-player-desktop.sh" "$STAGE/libexec/spyder/player/desktop/player"

# Android debug APK (🎯T102): best-effort. player/android's native build
# (build-deps.sh) is currently broken pending the sokol_gfx port (🎯T34),
# so a fresh `gradlew assembleDebug` is not reliably producible here.
# Package an already-built debug APK when one exists under
# player/android/app/build/outputs/apk/debug/; otherwise skip with a
# warning rather than failing the whole release — launch_player's error
# message (path=/SPYDER_PLAYER_ANDROID) still names the fix.
ANDROID_APK="$ROOT/player/android/app/build/outputs/apk/debug/app-debug.apk"
if [[ -f "$ANDROID_APK" ]]; then
	echo "release-package: packaging android debug APK from $ANDROID_APK" >&2
	cp "$ANDROID_APK" "$STAGE/libexec/spyder/player/android/player.apk" 2>/dev/null ||
		{ mkdir -p "$STAGE/libexec/spyder/player/android" && cp "$ANDROID_APK" "$STAGE/libexec/spyder/player/android/player.apk"; }
else
	echo "release-package: WARNING no android debug APK at $ANDROID_APK — skipping (see 🎯T34; player/android/build-deps.sh is not yet ported to sokol)" >&2
fi

# Homebrew-releaser cds into the single top-level directory; keep that prefix.
mkdir -p "$DIST/$TARBALL_DIR"
mv "$STAGE/bin" "$STAGE/libexec" "$DIST/$TARBALL_DIR/"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$DIST/$TARBALL_DIR/"
rmdir "$STAGE"
tar -czf "$DIST/$ASSET" -C "$DIST" "$TARBALL_DIR"
rm -rf "$DIST/$TARBALL_DIR"

echo "wrote dist/$ASSET"
echo "release-package: ${TAG} → dist/"
