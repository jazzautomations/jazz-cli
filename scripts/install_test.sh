#!/bin/sh
# Focused installer platform-selection tests without network access.
set -eu
repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin"
cat > "$tmp/bin/uname" <<'EOF_UNAME'
#!/bin/sh
case "$1" in
  -s) echo Linux ;;
  -m) echo aarch64 ;;
  *) exit 1 ;;
esac
EOF_UNAME
cat > "$tmp/bin/curl" <<'EOF_CURL'
#!/bin/sh
exit 22
EOF_CURL
chmod +x "$tmp/bin/uname" "$tmp/bin/curl"

assert_asset() {
  want=$1
  shift
  if output=$(env PATH="$tmp/bin:$PATH" HOME="$tmp/home" "$@" sh "$repo/scripts/install.sh" --base-url http://invalid.test 2>&1); then
    echo "installer unexpectedly succeeded" >&2
    exit 1
  fi
  case "$output" in
    *"baixando $want de"*) ;;
    *) echo "expected $want selection; got: $output" >&2; exit 1 ;;
  esac
}

mkdir -p "$tmp/home"
assert_asset jazz-android-arm64 PREFIX=/data/data/com.termux/files/usr TERMUX_VERSION=
assert_asset jazz-android-arm64 PREFIX=/elsewhere/with/com.termux/files/usr TERMUX_VERSION=
assert_asset jazz-android-arm64 PREFIX= TERMUX_VERSION=0.118
assert_asset jazz-linux-arm64 PREFIX=/workspace/prefix TERMUX_VERSION=
echo 'installer platform selection: 4 cases passed'
