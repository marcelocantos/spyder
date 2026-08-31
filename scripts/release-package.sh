#!/usr/bin/env bash
# Build the darwin-arm64 release tarball into dist/.
# Layout matches the retired .github/workflows/release.yml (🎯T135):
#   spyder-<ver>-darwin-arm64/{bin/spyder, bin/spyder-killusbmuxd, libexec/spyder/ios}
set -euo pipefail
source "$(dirname "$0")/release-common.sh"

VERSION="$(require_version "${1:-}")"
TAG="v${VERSION}"
DIST="$ROOT/dist"
STAGE="$DIST/build"
TARBALL_DIR="spyder-${VERSION}-darwin-arm64"
ASSET="${TARBALL_DIR}.tar.gz"

rm -rf "$DIST"
mkdir -p "$STAGE/bin" "$STAGE/libexec/spyder"

echo "release-package: building spyder ${TAG} (darwin/arm64, CGO=1)" >&2
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	go build -trimpath -ldflags "-s -w -X main.version=${TAG}" \
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

# Homebrew-releaser cds into the single top-level directory; keep that prefix.
mkdir -p "$DIST/$TARBALL_DIR"
mv "$STAGE/bin" "$STAGE/libexec" "$DIST/$TARBALL_DIR/"
rmdir "$STAGE"
tar -czf "$DIST/$ASSET" -C "$DIST" "$TARBALL_DIR"
rm -rf "$DIST/$TARBALL_DIR"

echo "wrote dist/$ASSET"
echo "release-package: ${TAG} → dist/"
