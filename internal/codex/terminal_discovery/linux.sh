#!/bin/sh
set -eu

supported=false
printf 'SOURCES'
case "${XDG_SESSION_TYPE:-}" in wayland) printf '\twayland';; esac
if [ -n "${WAYLAND_DISPLAY:-}" ] && [ "${XDG_SESSION_TYPE:-}" != wayland ]; then printf '\twayland'; fi
printf '\n'

if [ -n "${DISPLAY:-}" ] && command -v xprop >/dev/null 2>&1; then
	clients=$(xprop -root _NET_CLIENT_LIST 2>/dev/null) || clients=
	active=$(xprop -root _NET_ACTIVE_WINDOW 2>/dev/null || true)
	active_id=$(printf '%s\n' "$active" | sed -n 's/.*# \(0x[[:xdigit:]]*\).*/\1/p')
	if [ -n "$clients" ]; then
		supported=true
		printf 'SOURCE\tx11-ewmh\n'
		printf '%s\n' "$clients" | grep -o '0x[[:xdigit:]]*' | head -n 256 | while IFS= read -r id; do
			[ -n "$id" ] || continue
			property=$(xprop -id "$id" _NET_WM_NAME 2>/dev/null || true)
			case "$property" in
				*' = "'*) ;;
				*) property=$(xprop -id "$id" WM_NAME 2>/dev/null || true) ;;
			esac
			value=${property#* = }
			[ -n "$value" ] || continue
			printf '%s\n' "$value" | grep -qE '^"[[:xdigit:]]{8}(-[[:xdigit:]]{1,4}){0,4}' || continue
			if [ "$id" = "$active_id" ]; then foreground=true; else foreground=false; fi
			printf 'x11-ewmh\t%s\t%s\tfalse\t\t%s\n' "$id" "$foreground" "$value"
		done
	fi
fi

if [ -n "${TMUX:-}" ] && [ -n "${TMUX_PANE:-}" ] && command -v tmux >/dev/null 2>&1; then
	case "$TMUX" in
		/*,*) socket=${TMUX%%,*} ;;
		*) socket= ;;
	esac
	case "$TMUX_PANE" in
		%*) pane_number=${TMUX_PANE#%} ;;
		*) pane_number= ;;
	esac
	case "$pane_number" in ''|*[!0-9]*) pane_number= ;; esac
	if [ -n "$socket" ] && [ -n "$pane_number" ]; then
		session=$(tmux -S "$socket" display-message -p -t "$TMUX_PANE" '#{session_id}' 2>/dev/null || true)
		case "$session" in
			\$[0-9]*)
				supported=true
				printf 'SOURCE\ttmux-current-session\n'
				separator=$(printf '\037')
				format=$(printf '#{pane_id}\037#{pane_title}\037#{pane_current_path}\037#{pane_active}')
				tmux -S "$socket" list-panes -s -t "$session" -F "$format" 2>/dev/null | head -n 256 |
				while IFS="$separator" read -r pane_id pane_title pane_path pane_active; do
					[ -n "$pane_id" ] || continue
					printf '%s\n' "$pane_title" | grep -qE '^[[:xdigit:]]{8}(-[[:xdigit:]]{1,4}){0,4}' || continue
					if [ "$pane_active" = 1 ] && [ "$pane_id" = "$TMUX_PANE" ]; then focused=true; else focused=false; fi
					printf 'tmux\t%s:%s\t%s\t%s\t%s\t%s\n' "$session" "$pane_id" "$focused" "$focused" "$pane_path" "$pane_title"
				done
				;;
		esac
	fi
fi

if [ "$supported" = true ]; then
	printf 'SUPPORTED\n'
else
	if [ "${XDG_SESSION_TYPE:-}" = wayland ] || [ -n "${WAYLAND_DISPLAY:-}" ]; then
		printf 'UNSUPPORTED\tWayland title enumeration is unavailable and no inherited tmux pane scope was found\n'
	else
		printf 'UNSUPPORTED\tNo usable X11 title API or inherited tmux pane scope is available\n'
	fi
fi
