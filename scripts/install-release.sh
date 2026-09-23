#!/usr/bin/env bash
set -euo pipefail

repo="atfa/duo"
os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "Duo supports macOS and Linux, not $os" >&2; exit 1 ;;
esac

case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "Duo supports amd64 and arm64, not $arch" >&2; exit 1 ;;
esac

command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v tar >/dev/null || { echo "tar is required" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/$repo/releases/latest/download/duo_${os}_${arch}.tar.gz"
curl -fL "$url" -o "$tmp/duo.tar.gz"
tar -xzf "$tmp/duo.tar.gz" -C "$tmp"

bin_dir="${HOME}/.local/bin"
extension_dir="${HOME}/.pi/agent/extensions/duo"
mkdir -p "$bin_dir" "$(dirname "$extension_dir")"
install -m 0755 "$tmp/duo" "$bin_dir/duo"
rm -rf "$extension_dir"
cp -R "$tmp/pi-extension" "$extension_dir"

echo "Installed Duo to $bin_dir/duo"
echo "Installed Pi bridge to $extension_dir"
echo "Run: cd /path/to/git/repo && duo"
