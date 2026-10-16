#!/usr/bin/env bash
set -u

terminal_mode=false
if [[ "${1:-}" == --terminal-picker ]]; then
	terminal_mode=true
	shift
fi

show_error() {
	message="$1"
	if [[ "$terminal_mode" == true ]]; then
		printf 'xduo could not start: %s\n' "$message" >&2
	elif command -v zenity >/dev/null 2>&1; then
		zenity --error --title='xduo could not start' --text="$message" >/dev/null 2>&1 || true
	elif command -v kdialog >/dev/null 2>&1; then
		kdialog --title 'xduo could not start' --error "$message" >/dev/null 2>&1 || true
	elif command -v yad >/dev/null 2>&1; then
		yad --title='xduo could not start' --error --text="$message" >/dev/null 2>&1 || true
	elif command -v xmessage >/dev/null 2>&1; then
		xmessage -center "$message" >/dev/null 2>&1 || true
	elif command -v notify-send >/dev/null 2>&1; then
		notify-send --urgency=critical 'xduo could not start' "$message" >/dev/null 2>&1 || true
	else
		printf 'xduo could not start: %s\n' "$message" >&2
	fi
}

pause_terminal() {
	if [[ "$terminal_mode" == true ]]; then
		printf '\nPress Enter to close this window. '
		IFS= read -r _ || true
	fi
}

choose_directory() {
	if command -v zenity >/dev/null 2>&1; then
		zenity --file-selection --directory --title='Choose a Git repository for this Duo session'
	elif command -v kdialog >/dev/null 2>&1; then
		kdialog --getexistingdirectory "$HOME" --title 'Choose a Git repository for this Duo session'
	elif command -v yad >/dev/null 2>&1; then
		yad --file-selection --directory --title='Choose a Git repository for this Duo session'
	else
		return 127
	fi
}

launch_terminal_picker() {
	if command -v x-terminal-emulator >/dev/null 2>&1; then
		x-terminal-emulator -e "$0" --terminal-picker
	elif command -v xfce4-terminal >/dev/null 2>&1; then
		xfce4-terminal --hold --execute "$0" --terminal-picker
	elif command -v gnome-terminal >/dev/null 2>&1; then
		gnome-terminal --wait -- "$0" --terminal-picker
	elif command -v konsole >/dev/null 2>&1; then
		konsole --hold -e "$0" --terminal-picker
	elif command -v xterm >/dev/null 2>&1; then
		xterm -hold -e "$0" --terminal-picker
	elif command -v kitty >/dev/null 2>&1; then
		kitty --hold "$0" --terminal-picker
	elif command -v alacritty >/dev/null 2>&1; then
		alacritty --hold -e "$0" --terminal-picker
	else
		return 127
	fi
}

if [[ -z "${DUO_HOST_BIN:-}" || ! -x "$DUO_HOST_BIN" ]]; then
	show_error 'The Duo command could not be found. Reinstall Duo and try again.'
	pause_terminal
	exit 1
fi
if [[ -z "${DUO_GPUI_BIN:-}" || ! -x "$DUO_GPUI_BIN" ]]; then
	show_error 'The xduo renderer could not be found. Reinstall Duo and try again.'
	pause_terminal
	exit 1
fi

if [[ "$terminal_mode" == true ]]; then
	printf 'Enter the path to a Git repository, or leave blank to cancel: '
	IFS= read -r project_dir || project_dir=''
	if [[ -z "$project_dir" ]]; then
		exit 0
	fi
else
	if [[ -n "${DUO_REPO:-}" ]]; then
		project_dir="$DUO_REPO"
	elif project_root="$(git -C "$PWD" rev-parse --show-toplevel 2>/dev/null)"; then
		project_dir="$project_root"
	else
		if project_dir="$(choose_directory 2>/dev/null)"; then
			:
		else
			chooser_status=$?
			if [[ $chooser_status -ne 127 ]]; then
				# Closing the chooser is a normal cancellation.
				exit 0
			fi
			if [[ -t 0 && -t 1 ]]; then
				terminal_mode=true
				printf 'Enter the path to a Git repository, or leave blank to cancel: '
				IFS= read -r project_dir || project_dir=''
				[[ -n "$project_dir" ]] || exit 0
			elif launch_terminal_picker; then
				exit 0
			else
				show_error 'No folder chooser or terminal emulator is available. Install zenity, kdialog, or yad, or start xduo from a Git repository.'
				exit 1
			fi
		fi
	fi
	if [[ -z "$project_dir" ]]; then
		exit 0
	fi
	if [[ ! -d "$project_dir" ]]; then
		show_error "The selected repository directory does not exist: $project_dir"
		pause_terminal
		exit 1
	fi
fi
if ! project_root="$(git -C "$project_dir" rev-parse --show-toplevel 2>/dev/null)"; then
	show_error 'Choose an existing Git repository. Duo needs a repository to start a session.'
	pause_terminal
	exit 1
fi
if ! cd "$project_root"; then
	show_error "Could not open the selected repository: $project_root"
	pause_terminal
	exit 1
fi

umask 077
log_dir="${XDG_STATE_HOME:-${HOME}/.local/state}/duo"
if ! mkdir -p "$log_dir"; then
	show_error "Could not create the Duo log directory: $log_dir"
	pause_terminal
	exit 1
fi
log_file="$log_dir/xduo.log"
if "$DUO_HOST_BIN" gui "$@" >"$log_file" 2>&1; then
	exit 0
else
	status=$?
fi
recent_output="$(tail -n 20 "$log_file" 2>/dev/null)"
message="$(printf 'Duo could not start (exit %s).\nRecent output:\n%s\nFull log: %s' "$status" "$recent_output" "$log_file")"
show_error "$message"
pause_terminal
exit "$status"
