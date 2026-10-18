#!/bin/sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
. "$root/scripts/xduo-launcher-path.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/xduo-path-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

mkdir -p "$tmp/nvm/bin" "$tmp/home/.local/bin" "$tmp/resources/bin"
for name in codex opencode pi; do
	: >"$tmp/nvm/bin/$name"
	chmod +x "$tmp/nvm/bin/$name"
done
: >"$tmp/home/.local/bin/agy"
chmod +x "$tmp/home/.local/bin/agy"

cat >"$tmp/login-shell" <<'SH'
#!/bin/sh
echo 'shell startup chatter'
echo "__DUO_XDUO_PATH__$DUO_TEST_LOGIN_PATH"
SH
chmod +x "$tmp/login-shell"

export HOME="$tmp/home"
export SHELL="$tmp/login-shell"
export DUO_TEST_LOGIN_PATH="$tmp/nvm/bin"
PATH=$(duo_xduo_launcher_path "$tmp/resources" "$tmp/resources/bin" /usr/bin:/bin)
export PATH
for name in codex opencode pi agy; do
	command -v "$name" >/dev/null || {
		echo "error: $name was not found in simulated Finder environment" >&2
		exit 1
	}
done

# A shell startup failure must retain the launcher's built-in fallback paths.
cat >"$tmp/failing-shell" <<'SH'
#!/bin/sh
exit 1
SH
chmod +x "$tmp/failing-shell"
SHELL="$tmp/failing-shell"
fallback=$(SHELL="$SHELL" duo_xduo_launcher_path "$tmp/resources" "$tmp/resources/bin" /custom/inherited)
case ":$fallback:" in
	*":$tmp/home/.local/bin:"*":/custom/inherited:"*) ;;
	*) echo 'error: fallback PATH was lost when login shell failed' >&2; exit 1 ;;
esac

echo 'xduo launcher PATH checks passed'
