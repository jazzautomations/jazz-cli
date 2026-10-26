#!/usr/bin/env bash
# Build signed release assets for `jazz update` / scripts/install.sh into dist/release:
#   jazz-<os>-<arch> for linux/386, linux/amd64, linux/arm64, android/arm64,
#   darwin/arm64, darwin/amd64, jazz-linux-armv7 (linux/arm, GOARM=7),
#   jazz-windows-<arch>.exe for windows/amd64, windows/arm64,
#   checksums.txt (sha256), manifest.txt (version + sha256, signed), VERSION,
#   install.sh and install.ps1 (both carrying the public key).
# Signing is mandatory unless JAZZ_UNSIGNED=1: create a key once with
#   go run ./scripts/sign -gen ~/.config/jazz-release.key
# and run with JAZZ_SIGN_KEY=~/.config/jazz-release.key.
# JAZZ_UNSIGNED=1 builds the same assets without signatures and without a key in
# the binaries/installers: the installers then check only the sha256 (and warn),
# and `jazz update` refuses those binaries' updates. Use it only for test builds.
# JAZZ_EXPECT_VERSION=X.Y.Z fails the build when main.go's version differs (CI
# passes the tag here). The binaries embed the
# public key, manifest.txt.sig / .minisig and checksums.txt.sig are written, and
# the published install.sh carries the key, so both `jazz update` and the
# installer refuse anything not signed by you.
# Serve the directory (e.g. `python3 -m http.server -d dist/release 8090`) and set
# "update_url": "http://<tailnet-ip>:8090" in the target's config.json (plain http
# is accepted only for loopback and tailnet 100.x addresses), or upload the
# files to a GitHub release.
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
UNSIGNED=${JAZZ_UNSIGNED:-}
PUB=""; MPUB=""
if [ "$UNSIGNED" = 1 ]; then
  echo "release.sh: AVISO: JAZZ_UNSIGNED=1, release sem assinatura (só para teste)" >&2
elif [ -z "${JAZZ_SIGN_KEY:-}" ] || [ ! -f "$JAZZ_SIGN_KEY" ]; then
  echo "release.sh: JAZZ_SIGN_KEY=<arquivo da chave> é obrigatório (go run ./scripts/sign -gen <arquivo>), ou JAZZ_UNSIGNED=1" >&2
  exit 1
else
  PUB=$("$GO" run ./scripts/sign -pub "$JAZZ_SIGN_KEY")
  MPUB=$("$GO" run ./scripts/sign -minisign-pub "$JAZZ_SIGN_KEY")
fi
if [ -n "${JAZZ_UPDATE_PUBKEY:-}" ] && [ "$JAZZ_UPDATE_PUBKEY" != "$PUB" ]; then
  echo "release.sh: JAZZ_UPDATE_PUBKEY não é a chave pública de JAZZ_SIGN_KEY" >&2
  exit 1
fi
VER=$(grep -m1 '^var version' main.go | cut -d'"' -f2)
if [ -n "${JAZZ_EXPECT_VERSION:-}" ] && [ "${JAZZ_EXPECT_VERSION#v}" != "$VER" ]; then
  echo "release.sh: a tag é ${JAZZ_EXPECT_VERSION#v} mas main.go diz $VER" >&2
  exit 1
fi
OUT=dist/release
rm -rf "$OUT"
mkdir -p "$OUT"
for t in linux/386 linux/amd64 linux/arm64 linux/arm android/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}
  ext=""; [ "$os" = windows ] && ext=".exe"
  # 32-bit ARM (Raspberry Pi 2/3 with a 32-bit OS, TV boxes): ARMv7 hard-float
  name=$arch; goarm=""
  if [ "$arch" = arm ]; then name=armv7; goarm=7; fi
  echo "› $os/$arch${goarm:+ (GOARM=$goarm)}"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=$goarm "$GO" build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=$VER -X main.updatePubKey=$PUB" -o "$OUT/jazz-$os-$name$ext" .
done
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$OUT" && sha256sum jazz-* > checksums.txt )
else
  ( cd "$OUT" && shasum -a 256 jazz-* > checksums.txt )
fi
{
  echo "jazz-cli release manifest v1"
  echo "version $VER"
  cat "$OUT/checksums.txt"
} > "$OUT/manifest.txt"
echo "$VER" > "$OUT/VERSION"
if [ "$UNSIGNED" = 1 ]; then
  cp scripts/install.sh "$OUT/install.sh"
  cp scripts/install.ps1 "$OUT/install.ps1"
else
  "$GO" run ./scripts/sign -key "$JAZZ_SIGN_KEY" "$OUT/manifest.txt" "$OUT/checksums.txt"
  echo "› manifest.txt e checksums.txt assinados"
  sed -e "s|^JAZZ_PUBKEY=.*|JAZZ_PUBKEY=\"$PUB\"|" -e "s|^JAZZ_MINISIGN_PUB=.*|JAZZ_MINISIGN_PUB=\"$MPUB\"|" scripts/install.sh > "$OUT/install.sh"
  grep -q "^JAZZ_PUBKEY=\"$PUB\"" "$OUT/install.sh" || { echo "release.sh: não consegui pôr a chave no install.sh" >&2; exit 1; }
  sed -e "s|^\$JazzPubKey = .*|\$JazzPubKey = '$PUB'|" scripts/install.ps1 > "$OUT/install.ps1"
  grep -q "^\$JazzPubKey = '$PUB'" "$OUT/install.ps1" || { echo "release.sh: não consegui pôr a chave no install.ps1" >&2; exit 1; }
fi
chmod 755 "$OUT/install.sh"
ls -lh "$OUT"
if [ "$UNSIGNED" = 1 ]; then echo "✓ release $VER (SEM assinatura) em $OUT"; else echo "✓ release $VER assinada em $OUT"; fi
