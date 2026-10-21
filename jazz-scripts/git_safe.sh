# git_safe: a few git changes that cannot lose work. No force, reset, clean or history rewrite.
#   stash         git stash push [-u] -m MESSAGE [-- PATHS]
#   branch        create and switch to a NEW branch NAME (refuses an existing one)
#   revert-file   restore PATHS to HEAD, keeping the old content in a stash (recoverable)
#   commit-paths  commit only PATHS with MESSAGE
command -v git >/dev/null 2>&1 || fail "git is not installed"
git rev-parse --git-dir >/dev/null 2>&1 || fail "not a git repository"
export GIT_TERMINAL_PROMPT=0 GIT_EDITOR=true GIT_PAGER=cat
op=${ARG_OP:-}
msg=${ARG_MESSAGE:-}
name=${ARG_NAME:-}

# paths: newline- or space-separated, never globbed, never an option
set -f
paths=$(printf '%s\n' "${ARG_PATHS:-}" | tr ' ' '\n' | awk 'length($0)')
for p in $paths; do
	case "$p" in
	-*) fail "a path may not start with '-': $p" ;;
	esac
done
need_paths() { [ -n "$paths" ] || fail "$op needs paths"; }

case "$op" in
stash)
	[ -n "$msg" ] || msg="jazz stash"
	u=""
	[ "${ARG_UNTRACKED:-false}" = true ] && u="-u"
	if [ -n "$paths" ]; then
		# shellcheck disable=SC2086
		out=$(git stash push $u -m "$msg" -- $paths 2>&1) || fail "git stash failed: $out"
	else
		out=$(git stash push $u -m "$msg" 2>&1) || fail "git stash failed: $out"
	fi
	case "$out" in
	*"No local changes"*) fail "nothing to stash" ;;
	esac
	emit_ok "$(printf '{"op":"stash","ref":"stash@{0}","output":%s}' "$(js "$out")")"
	;;
branch)
	case "$name" in
	'' | -*) fail "branch needs a name that does not start with '-'" ;;
	esac
	git check-ref-format --branch "$name" >/dev/null 2>&1 || fail "invalid branch name: $name"
	git show-ref --verify --quiet "refs/heads/$name" && fail "branch already exists: $name (refusing to overwrite)"
	out=$(git switch -c "$name" 2>&1) || out=$(git checkout -b "$name" 2>&1) || fail "could not create branch: $out"
	emit_ok "$(printf '{"op":"branch","branch":%s,"from":%s}' "$(js "$name")" "$(js "$(git rev-parse --short HEAD 2>/dev/null)")")"
	;;
revert-file)
	need_paths
	# shellcheck disable=SC2086
	out=$(git stash push -m "jazz revert-file backup" -- $paths 2>&1) || fail "git stash failed: $out"
	case "$out" in
	*"No local changes"*) fail "the paths have no changes to revert" ;;
	esac
	emit_ok "$(printf '{"op":"revert-file","paths":%s,"backup":"stash@{0}","restore_with":"git stash pop"}' "$(printf '%s\n' "$paths" | jarr 50)")"
	;;
commit-paths)
	need_paths
	[ -n "$msg" ] || fail "commit-paths needs a message"
	# shellcheck disable=SC2086
	out=$(git add -- $paths 2>&1) || fail "git add failed: $out"
	# shellcheck disable=SC2086
	out=$(git commit -q -m "$msg" -- $paths 2>&1) || fail "git commit failed: $out"
	emit_ok "$(printf '{"op":"commit-paths","commit":%s,"paths":%s}' "$(js "$(git rev-parse --short HEAD)")" "$(printf '%s\n' "$paths" | jarr 50)")"
	;;
*)
	fail "op must be stash, branch, revert-file or commit-paths (force, reset and clean are not offered)"
	;;
esac
