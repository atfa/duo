#!/bin/sh

duo_xduo_launcher_path() {
	resources=$1
	resources_bin=$2
	inherited_path=$3
	path=$resources:$resources_bin
	printf '%s' "$path:$HOME/.local/bin:$HOME/.cargo/bin:$HOME/.go/bin:$HOME/go/bin:$HOME/.bun/bin:$HOME/.volta/bin:$HOME/.asdf/shims:$HOME/.npm-global/bin:$HOME/.local/share/mise/shims:$HOME/.local/share/pnpm:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin:$inherited_path"
}
