#!/bin/sh
set -eu

VERSION="${1:?usage: scripts/build-deb.sh VERSION ARCH OUT_DIR}"
ARCH="${2:?usage: scripts/build-deb.sh VERSION ARCH OUT_DIR}"
OUT_DIR="${3:?usage: scripts/build-deb.sh VERSION ARCH OUT_DIR}"

case "$ARCH" in
  amd64) GOARCH=amd64 ;;
  arm64) GOARCH=arm64 ;;
  *) echo "unsupported deb arch: $ARCH" >&2; exit 2 ;;
esac

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BUILD_DIR="$ROOT/.dist/pkg/meerkat-agent_${VERSION}_${ARCH}"
PKG_DIR="$BUILD_DIR/pkg"
CONTROL_DIR="$PKG_DIR/DEBIAN"

rm -rf "$BUILD_DIR"
mkdir -p "$CONTROL_DIR" "$PKG_DIR/usr/bin" "$PKG_DIR/lib/systemd/system" "$OUT_DIR"

CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build \
  -trimpath \
  -ldflags "-s -w -X github.com/AndiOliverIon/meerkat-agent/internal/collect.Version=$VERSION" \
  -o "$PKG_DIR/usr/bin/meerkat-agent" \
  ./cmd/meerkat-agent

install -m 0644 "$ROOT/packaging/systemd/meerkat-agent.service" "$PKG_DIR/lib/systemd/system/meerkat-agent.service"

sed \
  -e "s/\${VERSION}/$VERSION/g" \
  -e "s/\${ARCH}/$ARCH/g" \
  "$ROOT/packaging/debian/control" > "$CONTROL_DIR/control"

install -m 0755 "$ROOT/packaging/debian/postinst" "$CONTROL_DIR/postinst"
install -m 0755 "$ROOT/packaging/debian/prerm" "$CONTROL_DIR/prerm"
install -m 0755 "$ROOT/packaging/debian/postrm" "$CONTROL_DIR/postrm"

dpkg-deb --build --root-owner-group "$PKG_DIR" "$OUT_DIR/meerkat-agent_${VERSION}_${ARCH}.deb"
