#!/bin/sh
# jazz-cli installer for Linux and macOS (POSIX sh, no root needed):
#
#   curl -fsSL https://github.com/jazzautomations/jazz-cli/releases/latest/download/install.sh | sh
#   curl -fsSL …/install.sh | sh -s -- --system          # /usr/local/bin (uses sudo)
#
# Downloads jazz-<os>-<arch> for this machine (linux 386/amd64/arm64, darwin
# amd64/arm64) from the GitHub release of jazzautomations/jazz-cli, verifies the
# release signature (manifest.txt.sig: Ed25519, public key embedded below by
# scripts/release.sh) and the binary's sha256 from the signed manifest, and
# installs it to ~/.local/bin.
#
# The signature is checked with minisign (minisign -V) or OpenSSL 3 (ed25519).
# Without either tool, or with a copy of this script that carries no key (the
# one in the git repository), only the sha256 is checked and a warning is
# printed; --require-sig (JAZZ_REQUIRE_SIG=1) turns that into an error.
#
# Options (environment variable in brackets):
#   --bin-dir DIR     install directory               [JAZZ_BIN_DIR]  default ~/.local/bin
#   --system          install to /usr/local/bin, with sudo when needed
#   --version vX.Y.Z  a specific release instead of the latest   [JAZZ_VERSION]
#   --base-url URL    folder with manifest.txt(.sig) and the binaries [JAZZ_BASE_URL]
#   --require-sig     fail when the signature cannot be verified  [JAZZ_REQUIRE_SIG=1]
#   -h, --help        this help
# Also: JAZZ_OPENSSL=<path to an OpenSSL 3 binary> when `openssl` on PATH is
# LibreSSL or OpenSSL 1.x (macOS ships LibreSSL).
set -eu

REPO=jazzautomations/jazz-cli
# filled in by scripts/release.sh (base64 ed25519 public key, minisign -P key)
JAZZ_PUBKEY=""
JAZZ_MINISIGN_PUB=""

say() { printf '%s\n' "$*"; }
die() { printf 'jazz: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF_USAGE'
uso: install.sh [--bin-dir DIR | --system] [--version vX.Y.Z] [--base-url URL] [--require-sig]

  --bin-dir DIR     pasta de instalação (JAZZ_BIN_DIR; padrão ~/.local/bin)
  --system          instala em /usr/local/bin, com sudo se precisar
  --version vX.Y.Z  uma release específica em vez da última (JAZZ_VERSION)
  --base-url URL    pasta com manifest.txt(.sig) e os binários (JAZZ_BASE_URL)
  --require-sig     falha se a assinatura não puder ser verificada (JAZZ_REQUIRE_SIG=1)
  JAZZ_OPENSSL      caminho de um OpenSSL 3 (o openssl do macOS é LibreSSL)
EOF_USAGE
}

BIN_DIR=${JAZZ_BIN_DIR:-}
VERSION=${JAZZ_VERSION:-}
BASE=${JAZZ_BASE_URL:-}
REQUIRE_SIG=${JAZZ_REQUIRE_SIG:-}
SYSTEM=""
while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir) [ $# -ge 2 ] || die "--bin-dir precisa de uma pasta"; BIN_DIR=$2; shift ;;
    --bin-dir=*) BIN_DIR=${1#*=} ;;
    --system|--sudo) SYSTEM=1 ;;
    --version) [ $# -ge 2 ] || die "--version precisa de uma versão (ex.: v0.5.0)"; VERSION=$2; shift ;;
    --version=*) VERSION=${1#*=} ;;
    --base-url) [ $# -ge 2 ] || die "--base-url precisa de uma URL"; BASE=$2; shift ;;
    --base-url=*) BASE=${1#*=} ;;
    --require-sig) REQUIRE_SIG=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "opção desconhecida: $1 (veja --help)" ;;
  esac
  shift
done
if [ -n "$SYSTEM" ] && [ -z "$BIN_DIR" ]; then BIN_DIR=/usr/local/bin; fi
[ -n "$BIN_DIR" ] || BIN_DIR="$HOME/.local/bin"

# ---- platform ----
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) die "sistema não suportado: $os (no Windows use install.ps1)" ;;
esac
arch=$(uname -m)
case "$arch" in
  i386|i486|i586|i686|x86) arch=386 ;;
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "arquitetura não suportada: $arch (há binários para 386, amd64 e arm64)" ;;
esac
# 32-bit userland on a 64-bit kernel: the amd64 binary would not run
if [ "$os" = linux ] && [ "$arch" = amd64 ] && [ "$(getconf LONG_BIT 2>/dev/null || echo 64)" = 32 ]; then
  arch=386
fi
# a shell under Rosetta reports x86_64 on Apple Silicon: prefer the native build
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi
[ "$os/$arch" != darwin/386 ] || die "macOS 32 bits não é suportado"
asset="jazz-$os-$arch"

if [ -n "$BASE" ]; then
  base=${BASE%/}
elif [ -n "$VERSION" ]; then
  case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
  base="https://github.com/$REPO/releases/download/$VERSION"
else
  base="https://github.com/$REPO/releases/latest/download"
fi

# ---- download ----
fetch() { # fetch URL OUT
  case "$1" in
    https://*) cproto='=https'; wsec='--https-only' ;;
    *) cproto='=http,https'; wsec='' ;;
  esac
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --proto "$cproto" --proto-redir "$cproto" --retry 2 "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    # shellcheck disable=SC2086 # $wsec is empty or one flag
    wget -q $wsec -O "$2" "$1"
  else
    die "preciso de curl ou wget"
  fi
}

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t jazz-install)
trap 'rm -rf "$tmp"' EXIT
trap 'rm -rf "$tmp"; exit 130' INT TERM
say "› baixando $asset de $base"
fetch "$base/$asset" "$tmp/jazz" || die "não consegui baixar $base/$asset"
fetch "$base/manifest.txt" "$tmp/manifest.txt" || die "não consegui baixar $base/manifest.txt"
have_sig=""
if fetch "$base/manifest.txt.sig" "$tmp/manifest.txt.sig" 2>/dev/null && [ -s "$tmp/manifest.txt.sig" ]; then
  have_sig=1
fi
fetch "$base/manifest.txt.minisig" "$tmp/manifest.txt.minisig" 2>/dev/null || rm -f "$tmp/manifest.txt.minisig"

# ---- signature ----
pick_openssl() {
  for o in "${JAZZ_OPENSSL:-}" openssl /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
    [ -n "$o" ] || continue
    command -v "$o" >/dev/null 2>&1 || continue
    if "$o" version 2>/dev/null | grep -q '^OpenSSL [3-9]'; then
      printf '%s' "$o"
      return 0
    fi
  done
  return 1
}

verified=""
why=""
if [ -z "$JAZZ_PUBKEY" ]; then
  why="este install.sh não tem chave pública (cópia do repositório, não da release)"
elif [ -z "$have_sig" ]; then
  # a release installer carries the key, so the release must be signed
  die "a release não tem manifest.txt.sig: instalação abortada"
elif [ -n "$JAZZ_MINISIGN_PUB" ] && [ -s "$tmp/manifest.txt.minisig" ] && command -v minisign >/dev/null 2>&1; then
  if minisign -Vq -P "$JAZZ_MINISIGN_PUB" -m "$tmp/manifest.txt" -x "$tmp/manifest.txt.minisig" >/dev/null 2>&1; then
    verified=minisign
  else
    die "assinatura da release inválida (minisign): instalação abortada"
  fi
elif ossl=$(pick_openssl); then
  # SubjectPublicKeyInfo for ed25519 = fixed 12-byte prefix + the 32-byte key
  { echo "-----BEGIN PUBLIC KEY-----"; echo "MCowBQYDK2VwAyEA$JAZZ_PUBKEY"; echo "-----END PUBLIC KEY-----"; } > "$tmp/pub.pem"
  "$ossl" base64 -d -A -in "$tmp/manifest.txt.sig" -out "$tmp/manifest.sig.bin" 2>/dev/null ||
    die "assinatura da release ilegível: instalação abortada"
  if "$ossl" pkeyutl -verify -pubin -inkey "$tmp/pub.pem" -rawin -in "$tmp/manifest.txt" -sigfile "$tmp/manifest.sig.bin" >/dev/null 2>&1; then
    verified=openssl
  else
    die "assinatura da release inválida (openssl): instalação abortada"
  fi
else
  why="sem minisign nem OpenSSL 3 para conferir a assinatura (JAZZ_OPENSSL=<caminho> aponta um OpenSSL 3)"
fi
if [ -n "$verified" ]; then
  say "› assinatura ok ($verified)"
elif [ "$REQUIRE_SIG" = 1 ]; then
  die "assinatura não verificada: $why (--require-sig)"
else
  printf 'jazz: aviso: assinatura NÃO verificada: %s; confiro só o sha256\n' "$why" >&2
fi

# ---- sha256 from the manifest ----
[ "$(head -n1 "$tmp/manifest.txt" | tr -d '\r')" = "jazz-cli release manifest v1" ] || die "manifest.txt inválido"
want=$(tr -d '\r' < "$tmp/manifest.txt" | awk -v a="$asset" '$2 == a || $2 == "*" a {print $1; exit}')
[ -n "$want" ] || die "o manifesto não lista $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/jazz" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/jazz" | awk '{print $1}')
elif ossl=$(command -v openssl); then
  got=$("$ossl" dgst -sha256 "$tmp/jazz" | awk '{print $NF}')
else
  die "preciso de sha256sum, shasum ou openssl para conferir o download"
fi
[ "$want" = "$got" ] || die "sha256 não confere (esperado $want, recebido $got)"
say "› sha256 ok"

# ---- install ----
chmod 755 "$tmp/jazz"
SUDO=""
priv() { if [ -n "$SUDO" ]; then sudo "$@"; else "$@"; fi; }
if ! mkdir -p "$BIN_DIR" 2>/dev/null || [ ! -w "$BIN_DIR" ]; then
  if [ -n "$SYSTEM" ] && [ "$(id -u)" != 0 ] && command -v sudo >/dev/null 2>&1; then
    SUDO=sudo
    say "› $BIN_DIR precisa de sudo"
    priv mkdir -p "$BIN_DIR"
  else
    die "não consigo gravar em $BIN_DIR (use --bin-dir, ou --system para usar sudo)"
  fi
fi
# copy next to the target, then rename: a running jazz is replaced atomically
priv cp "$tmp/jazz" "$BIN_DIR/.jazz.new"
priv chmod 755 "$BIN_DIR/.jazz.new"
priv mv -f "$BIN_DIR/.jazz.new" "$BIN_DIR/jazz"
say "✓ jazz instalado em $BIN_DIR/jazz"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    case "$(basename "${SHELL:-sh}")" in
      zsh) rc="$HOME/.zshrc" ;;
      bash) if [ "$os" = darwin ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi ;;
      fish) rc='' ;;
      *) rc="$HOME/.profile" ;;
    esac
    say "  $BIN_DIR não está no PATH. Adicione:"
    if [ -n "$rc" ]; then
      say "    echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> $rc"
    else
      say "    fish_add_path $BIN_DIR"
    fi
    say "  e abra um terminal novo."
    ;;
esac
"$BIN_DIR/jazz" version 2>/dev/null || true
say "  próximos passos: jazz doctor · jazz omni setup · jazz (na pasta do projeto)"
