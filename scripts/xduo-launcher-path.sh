#!/bin/sh

# Print a PATH suitable for xduo when launched by Finder. Node based agent
# commands are often installed in a version manager's shell configuration,
# which is not present in Finder's environment.
duo_xduo_user_path() {
	[ -n "${SHELL:-}" ] && [ -x "$SHELL" ] || return 1
	output=$(/usr/bin/mktemp "${TMPDIR:-/tmp}/duo-xduo-path.XXXXXX") || return 1

	# Interactive login shells load the same startup files users configure for
	# nvm, mise, and similar tools. Extract the marked line so startup chatter
	# on stdout cannot become part of PATH. A watchdog prevents a broken shell
	# startup file from keeping the desktop app from opening indefinitely.
	"$SHELL" -lic 'printf "\n__DUO_XDUO_PATH__%s\n" "$PATH"' >"$output" 2>/dev/null &
	shell_pid=$!
	(
		sleep 5
		kill "$shell_pid" 2>/dev/null || true
	) &
	watchdog_pid=$!
	if wait "$shell_pid"; then
		shell_status=0
	else
		shell_status=$?
	fi
	kill "$watchdog_pid" 2>/dev/null || true
	wait "$watchdog_pid" 2>/dev/null || true
	if [ "$shell_status" -eq 0 ]; then
		/usr/bin/sed -n 's/^__DUO_XDUO_PATH__//p' "$output" | /usr/bin/tail -n 1
	else
		rm -f "$output"
		return 1
	fi
	rm -f "$output"
}

duo_xduo_launcher_path() {
	resources=$1
	resources_bin=$2
	inherited_path=$3
	user_path=$(duo_xduo_user_path 2>/dev/null || true)
	path=$resources:$resources_bin
	[ -z "$user_path" ] || path="$path:$user_path"
	printf '%s' "$path:$HOME/.local/bin:$HOME/.cargo/bin:$HOME/.go/bin:$HOME/go/bin:$HOME/.bun/bin:$HOME/.volta/bin:$HOME/.asdf/shims:$HOME/.npm-global/bin:$HOME/.local/share/mise/shims:$HOME/.local/share/pnpm:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin:$inherited_path"
}
