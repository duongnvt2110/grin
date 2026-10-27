#!/bin/sh
set -eu

supported=false
complete=true
scanned=0
candidates=0
max_surfaces=4096
max_candidates=256
stop=false

printf 'SOURCES'
case "${XDG_SESSION_TYPE:-}" in wayland) printf '\twayland';; esac
if [ -n "${WAYLAND_DISPLAY:-}" ] && [ "${XDG_SESSION_TYPE:-}" != wayland ]; then printf '\twayland'; fi
printf '\n'

if [ -n "${DISPLAY:-}" ] && command -v xprop >/dev/null 2>&1; then
	printf 'SOURCE\tx11-ewmh\n'
	if clients=$(xprop -root _NET_CLIENT_LIST 2>/dev/null); then
		supported=true
	else
		complete=false
		clients=
	fi
	active=$(xprop -root _NET_ACTIVE_WINDOW 2>/dev/null || true)
	active_id=$(printf '%s\n' "$active" | sed -n 's/.*# \(0x[[:xdigit:]]*\).*/\1/p')
	window_ids=$(printf '%s\n' "$clients" | grep -o '0x[[:xdigit:]]*' || true)
	for id in $window_ids; do
		if [ "$scanned" -ge "$max_surfaces" ]; then complete=false; stop=true; break; fi
		scanned=$((scanned + 1))
		if property=$(xprop -id "$id" _NET_WM_NAME 2>/dev/null); then
			case "$property" in *' = "'*) ;; *) property=$(xprop -id "$id" WM_NAME 2>/dev/null) || { complete=false; continue; } ;; esac
		else
			property=$(xprop -id "$id" WM_NAME 2>/dev/null) || { complete=false; continue; }
		fi
		value=${property#* = }
		[ -n "$value" ] || continue
		printf '%s\n' "$value" | grep -qE '[[:xdigit:]]{8}(-[[:xdigit:]]{1,4}){0,4}' || continue
		if [ "$candidates" -ge "$max_candidates" ]; then complete=false; stop=true; break; fi
		candidates=$((candidates + 1))
		if [ "$id" = "$active_id" ]; then foreground=true; else foreground=false; fi
		printf 'x11-ewmh\t%s\t%s\tfalse\t\t%s\n' "$id" "$foreground" "$value"
	done
fi

if [ "$stop" = false ] && [ -n "${TMUX:-}" ] && [ -n "${TMUX_PANE:-}" ]; then
	case "$TMUX" in /*,*) socket=${TMUX%%,*} ;; *) socket= ;; esac
	case "$TMUX_PANE" in %*) pane_number=${TMUX_PANE#%} ;; *) pane_number= ;; esac
	case "$pane_number" in ''|*[!0-9]*) pane_number= ;; esac
	if [ -n "$socket" ] && [ -n "$pane_number" ]; then
		printf 'SOURCE\ttmux-current-session\n'
		if ! command -v tmux >/dev/null 2>&1; then
			complete=false
		else
			session=$(tmux -S "$socket" display-message -p -t "$TMUX_PANE" '#{session_id}' 2>/dev/null) || { complete=false; session=; }
			case "$session" in
				\$[0-9]*)
					if pane_rows=$(tmux -S "$socket" list-panes -s -t "$session" -F "$(printf '#{pane_id}\037#{pane_title}\037#{pane_current_path}\037#{pane_active')" 2>/dev/null); then
						supported=true
						separator=$(printf '\037')
						while IFS="$separator" read -r pane_id pane_title pane_path pane_active; do
							[ -n "$pane_id" ] || continue
							if [ "$scanned" -ge "$max_surfaces" ]; then complete=false; stop=true; break; fi
							scanned=$((scanned + 1))
							printf '%s\n' "$pane_title" | grep -qE '[[:xdigit:]]{8}(-[[:xdigit:]]{1,4}){0,4}' || continue
							if [ "$candidates" -ge "$max_candidates" ]; then complete=false; stop=true; break; fi
							candidates=$((candidates + 1))
							if [ "$pane_active" = 1 ] && [ "$pane_id" = "$TMUX_PANE" ]; then focused=true; else focused=false; fi
							printf 'tmux\t%s:%s\t%s\t%s\t%s\t%s\n' "$session" "$pane_id" "$focused" "$focused" "$pane_path" "$pane_title"
						done <<EOF
$pane_rows
EOF
					else
						complete=false
					fi
					;;
				*) complete=false ;;
			esac
		fi
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
printf 'COMPLETE\t%s\n' "$complete"
