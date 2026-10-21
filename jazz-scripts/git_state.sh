# git_state: branch, upstream ahead/behind, staged/unstaged/untracked, recent commits, conflicts.
command -v git >/dev/null 2>&1 || fail "git is not installed"
gitdir=$(git rev-parse --git-dir 2>/dev/null) || fail "not a git repository"
branch=$(git symbolic-ref --short -q HEAD 2>/dev/null)
detached=false
if [ -z "$branch" ]; then
	detached=true
	branch=$(git rev-parse --short HEAD 2>/dev/null)
fi
upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)
ahead=0
behind=0
if [ -n "$upstream" ]; then
	set -- $(git rev-list --left-right --count 'HEAD...@{u}' 2>/dev/null)
	ahead=${1:-0}
	behind=${2:-0}
fi
st=$(git status --porcelain 2>/dev/null)
conf_re='^(DD|AU|UD|UA|DU|AA|UU) '
conflicts=$(printf '%s\n' "$st" | grep -E "$conf_re" | cut -c4-)
staged=$(printf '%s\n' "$st" | grep -Ev "$conf_re" | grep -E '^[MADRCT]' | cut -c4-)
unstaged=$(printf '%s\n' "$st" | grep -Ev "$conf_re" | grep -E '^.[MDT]' | cut -c4-)
untracked=$(printf '%s\n' "$st" | grep -E '^\?\?' | cut -c4-)
op=""
[ -f "$gitdir/MERGE_HEAD" ] && op=merge
[ -d "$gitdir/rebase-merge" ] || [ -d "$gitdir/rebase-apply" ] && op=rebase
[ -f "$gitdir/CHERRY_PICK_HEAD" ] && op=cherry-pick
[ -f "$gitdir/REVERT_HEAD" ] && op=revert
[ -f "$gitdir/BISECT_LOG" ] && op=bisect
commits=$(git log -5 --format='%h %ad %s' --date=short 2>/dev/null)
stashes=$(git stash list 2>/dev/null | count)
data=$(printf '{"branch":%s,"detached":%s,"upstream":%s,"ahead":%s,"behind":%s,"staged":%s,"staged_count":%s,"unstaged":%s,"unstaged_count":%s,"untracked":%s,"untracked_count":%s,"conflicts":%s,"in_progress":%s,"stashes":%s,"last_commits":%s}' \
	"$(js "$branch")" "$detached" "$(js "$upstream")" "$(num "$ahead")" "$(num "$behind")" \
	"$(printf '%s\n' "$staged" | jarr 20)" "$(printf '%s\n' "$staged" | count)" \
	"$(printf '%s\n' "$unstaged" | jarr 20)" "$(printf '%s\n' "$unstaged" | count)" \
	"$(printf '%s\n' "$untracked" | jarr 20)" "$(printf '%s\n' "$untracked" | count)" \
	"$(printf '%s\n' "$conflicts" | jarr 50)" "$(js "$op")" "$(num "$stashes")" "$(printf '%s\n' "$commits" | jarr 5)")
emit_ok "$data"
