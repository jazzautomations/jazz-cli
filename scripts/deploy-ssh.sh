#!/usr/bin/env bash
# Cross-compile jazz on this machine and install it on another one over SSH
# (handy for an old linux/386 laptop that is too slow to build Go itself).
#
#   JAZZ_DEPLOY_HOST=<ssh-alias> scripts/deploy-ssh.sh          installs to ~/.local/bin/jazz
#                                    (no sudo) and makes sure ~/.local/bin is on PATH (~/.bashrc)
#   JAZZ_DEPLOY_HOST=<ssh-alias> scripts/deploy-ssh.sh --sudo   installs to /usr/local/bin/jazz
#                                    (sudo asks for the password on the remote tty)
#
#   JAZZ_DEPLOY_OS   target GOOS   (default linux)
#   JAZZ_DEPLOY_ARCH target GOARCH (default 386)
#   GO               go binary     (default: go on PATH)
#
# Key auth only: the script never reads, stores or passes a password.
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
HOST=${JAZZ_DEPLOY_HOST:?set JAZZ_DEPLOY_HOST=<ssh alias of the target machine>}
DOS=${JAZZ_DEPLOY_OS:-linux}
DARCH=${JAZZ_DEPLOY_ARCH:-386}
MODE=user
[ "${1:-}" = "--sudo" ] && MODE=sudo
VER=$(grep -m1 '^var version' main.go | cut -d'"' -f2)
echo "› build jazz $VER $DOS/$DARCH"
mkdir -p dist
CGO_ENABLED=0 GOOS=$DOS GOARCH=$DARCH "$GO" build -trimpath -ldflags="-s -w -X main.version=$VER" -o "dist/jazz-$DOS-$DARCH" .
ls -lh "dist/jazz-$DOS-$DARCH" | awk '{print "  " $5}'
scp -q "dist/jazz-$DOS-$DARCH" "$HOST":/tmp/jazz.new
scp -q scripts/jazz-cli.desktop "$HOST":/tmp/jazz-cli.desktop
if [ "$MODE" = sudo ]; then
  # -t gives sudo a terminal on the remote side so it can prompt there
  ssh -t "$HOST" 'sudo install -m 755 /tmp/jazz.new /usr/local/bin/jazz && rm -f /tmp/jazz.new'
  DEST=/usr/local/bin/jazz
else
  ssh "$HOST" 'set -e
    mkdir -p "$HOME/.local/bin"
    install -m 755 /tmp/jazz.new "$HOME/.local/bin/jazz.tmp"
    mv -f "$HOME/.local/bin/jazz.tmp" "$HOME/.local/bin/jazz"
    rm -f /tmp/jazz.new
    if ! grep -qs "jazz-cli: PATH" "$HOME/.bashrc"; then
      printf "\n# jazz-cli: PATH\ncase \":\$PATH:\" in *\":\$HOME/.local/bin:\"*) ;; *) export PATH=\"\$HOME/.local/bin:\$PATH\" ;; esac\n" >> "$HOME/.bashrc"
    fi'
  DEST='~/.local/bin/jazz'
fi
ssh "$HOST" 'mkdir -p ~/.local/share/applications && mv /tmp/jazz-cli.desktop ~/.local/share/applications/ && PATH="$HOME/.local/bin:$PATH" jazz version'
echo "✓ instalado: $HOST:$DEST"
