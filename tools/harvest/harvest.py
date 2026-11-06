#!/usr/bin/env python3
"""harvest: runs refused corpus rows ONLINE so verified plans accumulate
as witnesses in a SHARED plan cache — same fixture slug, same file, so
natural corpus variants ("compress docs" / "compress src") merge into
distillable shapes. After the pass, `jazz distill --list` shows what the
teacher taught; `jazz distill` promotes to offline methods.

Cost control: HARVEST_LIMIT / HARVEST_PAR / HARVEST_ONLY env knobs.
"""
import json, subprocess, os, sys, re, time, concurrent.futures, hashlib

RESULTS = os.environ.get('HARVEST_RESULTS', '/tmp/jazz-v5-work/results.v12.jsonl')
OUT = os.environ.get('HARVEST_OUT', '/tmp/jazz-v5-work/harvest.v1.jsonl')
JH = os.environ.get('HARVEST_JH', '/tmp/jazz-v5-work/jh-harvest')
FIXTURE = os.environ.get('HARVEST_FIXTURE', '/tmp/jazz-v5-work/fixture')
BIN = os.environ.get('HARVEST_BIN', '/home/h/.local/bin/jazz')
LIMIT = int(os.environ.get('HARVEST_LIMIT', '200'))
MODEL = os.environ.get('HARVEST_MODEL', 'anthropic/claude-sonnet-5-5')
PAR = int(os.environ.get('HARVEST_PAR', '2'))
ONLY = os.environ.get('HARVEST_ONLY', '')  # regex filter on req
# distillable style: learned methods may only contain replay-safe commands
# (read-only or known build/test tools — mutating lines never distill, by
# design). Teach the teacher to leave a replayable trail: short single-purpose
# read-only probes, no cd prefix, no ;-joins, no redirects.
# distillable style lives in FIXTURE/AGENTS.md (loaded as project
# instructions) — the request text stays clean so the learned method's
# task/examples aren't polluted by meta-guidance
HINT = ''

# pcMinSteps=12: plans under ~12 steps never distill (a literal replay of
# a 3-step task teaches nothing a method couldn't). So the harvest targets
# COMPOUND/multi-step rows — "find X and delete/transform each" — where an
# online run naturally crosses the gate. Simple refusals are method work.
COMPOUND = re.compile(r'(\be\b|\band\b|\bthen\b|depois|;|,\s*(e|and)\b|each|every|cada|todos|all\b)', re.I)
FEASIBLE = re.compile(
    r'(arquiv|file|pasta|director|diretor|folder|\bdir\b|find|busca|procura|search|'
    r'delet|remove|apaga|erase|permission|permiss|owner|dono|usuário|user|group|grupo|'
    r'linha|\bline\b|count|conta|quantos|how many|list|lista|size|tamanho|modif|recent|'
    r'empty|vazio|name|nome|ext|newer|older|maior|mais novo|\.[a-z]{1,4}\b)', re.I)
REPEAT = int(os.environ.get('HARVEST_REPEAT', '1'))  # runs per row: 2 closes wins>=2
PLACEHOLDER = re.compile(r'(\$[A-Za-z_0-9]+|<[a-z_]+>)')
# outside-root rows always deny by design — don't spend a run on them
OUTROOT = re.compile(r'(/home/|/proc|/usr\b|/tmp/|/new/|entire file|whole (file|dir)|home director|/etc\b|system root|root director)', re.I)
SKIP = re.compile(
    r'(systemctl|service|docker|ssh\b|http|url|download|install|network|ping|kill|'
    r'processo|process|git |commit|branch|merge|package|mount|inode|cron|'
    r'environment variable|env var|md5|sha|compress|zip|tar\b|regex|sed|awk|'
    r'sort\b|uniq|column|json|xml|base64|encode|decode|translate|explain|what does|'
    r'print.*(first|last) [0-9]+ (line|char|word)|reverse|uppercase|lowercase)', re.I)


def shape_of(req):
    """rough entity-masked shape for reporting — digits/quotes/paths -> $"""
    s = re.sub(r'"[^"]*"|\'[^\']*\'', '$S', req.lower())
    s = re.sub(r'\d+', '$N', s)
    s = re.sub(r'/[\w./-]+', '$P', s)
    s = re.sub(r'\*?\.[a-z]{1,4}\b', '.$E', s)
    return hashlib.md5(s.encode()).hexdigest()[:10]


rows = [json.loads(l) for l in open(RESULTS)]
todo = []
for r in rows:
    if r.get('status') not in ('refused', 'failed', 'needs_input'):
        continue
    q = r['req']
    if ONLY and not re.search(ONLY, q, re.I):
        continue
    if FEASIBLE.search(q) and not SKIP.search(q) and not PLACEHOLDER.search(q) and not OUTROOT.search(q):
        score = 2 if COMPOUND.search(q) else 1
        todo.append((score, r))
todo.sort(key=lambda x: -x[0])
todo = [r for _, r in todo[:LIMIT]]
todo = [dict(r, id=r['id'] + (f'~{n}' if n else '')) for n in range(REPEAT) for r in todo]
print(f'harvest: {len(todo)} rows (limit {LIMIT}, par {PAR})', flush=True)

os.makedirs(JH, exist_ok=True)

def run_one(r):
    env = dict(os.environ)
    env.update({
        'JAZZ_HOME': JH,
        'JAZZ_NO_UPDATE_CHECK': '1',
        'JAZZ_ONBOARD': 'off',
        # harvest: every verified run is a training example — keep even the
        # short procedures (default floor is 12 steps, too high for teaching)
        'JAZZ_PC_MIN_STEPS': '1',
        'TERM': 'dumb',
    })
    t0 = time.time()
    try:
        p = subprocess.run(
            [BIN, '-m', MODEL, '-p', '--timeout', '90s', r['req']],
            cwd=FIXTURE, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, timeout=120)
        out, code = p.stdout, p.returncode
    except Exception as e:
        out, code = str(e), -1
    tail = [l for l in out.splitlines() if l.strip()][-3:]
    return {**r, 'shape': shape_of(r['req']), 'online_exit': code,
            'online_ms': round((time.time() - t0) * 1000),
            'answer': ' | '.join(tail)[:300]}

done_ids = set()
if os.path.exists(OUT):
    for l in open(OUT, encoding='utf-8'):
        try:
            done_ids.add(json.loads(l)['id'])
        except ValueError:
            pass
todo = [r for r in todo if r['id'] not in done_ids]
print(f'resuming: {len(done_ids)} done, {len(todo)} to go', flush=True)

with open(OUT, 'a', encoding='utf-8') as f:
    with concurrent.futures.ThreadPoolExecutor(PAR) as ex:
        for i, r in enumerate(ex.map(run_one, todo)):
            f.write(json.dumps(r, ensure_ascii=False) + '\n')
            f.flush()
            if i % 10 == 0:
                print(f'  {i}/{len(todo)} {r["id"]} exit={r["online_exit"]} ({r["online_ms"]}ms)', flush=True)
print('done — now run: jazz distill --list', flush=True)
