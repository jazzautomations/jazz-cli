# jazz sh prelude (prepended to every sh script by the harness). POSIX sh (dash-safe).
# Args arrive as environment variables ARG_<NAME> (upper case); output is ONE JSON object.
ROOT=${JAZZ_ROOT:-$(pwd)}
cd "$ROOT" 2>/dev/null || true

# js STRING: print STRING as a JSON string literal
js() {
	printf '"'
	printf '%s' "$1" | tr -d '\001-\010\013-\037' | tr '\t' ' ' |
		sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' |
		awk 'NR > 1 { printf "%s", "\\n" } { printf "%s", $0 }'
	printf '"'
}

# jarr: stdin lines (non-empty, first $1 of them, default 20) as a JSON array of strings
jarr() {
	tr -d '\001-\010\013-\037' | tr '\t' ' ' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' |
		awk -v max="${1:-20}" 'BEGIN { printf "[" } length($0) && n < max { if (n++) printf ","; printf "\"%s\"", $0 } END { printf "]" }'
}

# num VALUE: VALUE as a JSON number (0 when empty or not numeric)
num() {
	case "$1" in
	'' | *[!0-9]*) printf 0 ;;
	*) printf '%s' "$1" ;;
	esac
}

# emit_ok DATA_JSON [WARNINGS_JSON]
emit_ok() {
	printf '{"ok":true,"confidence":1,"data":%s,"unparsed":[],"warnings":%s}' "$1" "${2:-[]}"
	exit 0
}

# fail MESSAGE [DATA_JSON]
fail() {
	_d=${2:-}
	[ -n "$_d" ] || _d='{}'
	printf '{"ok":false,"confidence":0,"data":%s,"unparsed":[],"warnings":[],"error":%s}' "$_d" "$(js "$1")"
	exit 0
}

# count: number of non-empty stdin lines
count() {
	awk 'length($0) { n++ } END { print n + 0 }'
}
