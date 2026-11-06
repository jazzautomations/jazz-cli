#!/usr/bin/env python3
"""Coverage runner: feeds each corpus request to the real offline brain and
records understood/intent/status from the session events jsonl."""
import json, subprocess, os, glob, re, sys, time
import concurrent.futures

WORK = os.environ.get('JAZZ_WORK', '/tmp/v5-work')
CORPUS = os.environ.get('BENCH_CORPUS', WORK + '/corpus/corpus-2k5.jsonl')
JH_ROOT = os.environ.get('BENCH_JH', WORK + '/jh')
FIXTURE = os.environ.get('BENCH_FIXTURE', WORK + '/fixture')
BIN = os.environ.get('BENCH_BIN', '/home/h/.local/bin/jazz')
OUT = os.environ.get('BENCH_OUT', WORK + '/results.jsonl')
# seed dir with learned methods: its data/forge/methods.overlay.json and
# trusted.json are copied into every per-row JAZZ_HOME before the run
SEED = os.environ.get('BENCH_SEED', WORK + '/jh-harvest')

BASE_ENV = {
    "HOME": "/tmp/jazz-v5-work/home",
    "PATH": "/usr/local/bin:/usr/bin:/bin",
    "LANG": "C.UTF-8",
    "TERM": "dumb",
    "JAZZ_OFFLINE": "1",
    "JAZZ_NO_UPDATE_CHECK": "1",
    "JAZZ_ONBOARD": "off",
}

def parse_events(jh):
    """Return (intent, status, guessed) from the newest session events file."""
    evs = sorted(glob.glob(os.path.join(jh, 'data/sessions/*/*.events.jsonl')),
                 key=os.path.getmtime)
    if not evs:
        return None, "no-session", None
    intent, status, guess = None, None, None
    for line in open(evs[-1], encoding='utf-8', errors='replace'):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        k = e.get('kind')
        if k == 'fastpath':
            intent = e.get('intent')
            status = 'succeeded' if e.get('exit') == 0 else 'fastpath-exit-%s' % e.get('exit')
        elif k == 'task':
            if e.get('intent'):
                intent = e['intent']
            status = e.get('to') or status
    return intent, (status or 'refused'), guess

SEED = os.environ.get('BENCH_SEED', WORK + '/jh-harvest')
def run_one(e):
    jh = os.path.join(JH_ROOT, e['id'])
    forge = os.path.join(jh, 'data', 'forge')
    os.makedirs(forge, exist_ok=True)
    import shutil
    for f in ('data/forge/methods.overlay.json', 'trusted.json'):
        src = os.path.join(SEED, f)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(jh, f))
    env = dict(BASE_ENV, JAZZ_HOME=jh)
    t0 = time.time()
    try:
        p = subprocess.run(
            [BIN, '-p', '--offline', '--timeout', '20s', e['req']],
            cwd=FIXTURE, env=env, stdin=subprocess.DEVNULL,
            capture_output=True, text=True, timeout=45)
        out, code = p.stdout + p.stderr, p.returncode
    except subprocess.TimeoutExpired:
        out, code = '', 'timeout'
    dur = time.time() - t0
    intent, status, _ = parse_events(jh)
    guess = None
    m = re.search(r'talvez ([a-z_0-9]+)', out)
    if m:
        guess = m.group(1)
    first = ''
    for l in out.splitlines():
        l = l.strip()
        if l and not l.startswith('jazz:'):
            first = l[:160]
            break
    return {**e, 'intent': intent, 'status': status, 'guess': guess,
            'understood': intent is not None, 'ms': round(dur * 1000),
            'exit': code, 'answer': first}

corpus = [json.loads(l) for l in open(CORPUS, encoding='utf-8')]
done_ids = set()
if os.path.exists(OUT):
    for l in open(OUT, encoding='utf-8'):
        done_ids.add(json.loads(l)['id'])
todo = [e for e in corpus if e['id'] not in done_ids]
print(f"corpus {len(corpus)} · done {len(done_ids)} · todo {len(todo)}", flush=True)

with open(OUT, 'a', encoding='utf-8') as f:
    with concurrent.futures.ThreadPoolExecutor(2) as ex:
        for i, r in enumerate(ex.map(run_one, todo)):
            f.write(json.dumps(r, ensure_ascii=False) + '\n')
            f.flush()
            if i % 20 == 0:
                print(f"  {i}/{len(todo)} {r['id']} -> {r['intent']}/{r['status']} ({r['ms']}ms)", flush=True)
print("done")
