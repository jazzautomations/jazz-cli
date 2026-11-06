#!/usr/bin/env python3
"""Compare two results.jsonl runs against the fixed verdict map.
Baseline understood ids carry a verdict; ids understood only in the new run
are listed for manual judgement."""
import json, os, collections, sys, importlib.util

spec = importlib.util.spec_from_file_location('verdicts', os.environ.get('JAZZ_WORK','/tmp/v5-work') + '/verdicts.py')
# verdicts.py prints at import; we just need VERDICT — read it directly
src = open(os.environ.get('JAZZ_WORK','/tmp/v5-work') + '/verdicts.py', encoding='utf-8').read()
import ast
tree = ast.parse(src)
VERDICT = next(ast.literal_eval(n.value) for n in tree.body
               if isinstance(n, ast.Assign) and any(
                   isinstance(t, ast.Name) and t.id == 'VERDICT' for t in n.targets))

def load(p):
    return {json.loads(l)['id']: json.loads(l) for l in open(p, encoding='utf-8')}

before = load(sys.argv[1] if len(sys.argv) > 1 else '/tmp/jazz-v5-work/results.baseline.jsonl')
after = load(sys.argv[2] if len(sys.argv) > 2 else '/tmp/jazz-v5-work/results.jsonl')

def summarize(rows):
    und = {i: r for i, r in rows.items() if r['understood']}
    v = collections.Counter()
    manual = []
    for i, r in und.items():
        if i in VERDICT:
            v[VERDICT[i][0]] += 1
        else:
            v['new-understood'] += 1
            manual.append(i)
    return und, v, manual

ub, vb, mb = summarize(before)
ua, va, ma = summarize(after)

print("BEFORE: understood %d/%d  verdicts %s" % (len(ub), len(before), dict(vb)))
print("AFTER : understood %d/%d  verdicts %s" % (len(ua), len(after), dict(va)))

flips_out = sorted(set(ub) - set(ua))
flips_in = sorted(set(ua) - set(ub))
print("\nflipped to NOT understood (%d):" % len(flips_out))
for i in flips_out:
    print("  %-7s was %-15s verdict=%-7s req=%s" % (i, ub[i]['intent'], VERDICT.get(i, ('?',))[0], ub[i]['req'][:80]))
print("\nflipped to understood (%d) [need verdict]:" % len(flips_in))
for i in flips_in:
    print("  %-7s now %-15s status=%-12s req=%s" % (i, ua[i]['intent'], ua[i]['status'], ua[i]['req'][:80]))

print("\nstill-understood FALSE matches after (%d):" % sum(1 for i in ua if i in VERDICT and VERDICT[i][0]=='false'))
to = collections.Counter()
for i in ua:
    if i in VERDICT and VERDICT[i][0] == 'false':
        to[ua[i]['intent']] += 1
        print("  %-7s %-15s status=%-12s req=%s" % (i, ua[i]['intent'], ua[i]['status'], ua[i]['req'][:80]))
print("\nfalse by wrongly-chosen intent:", dict(to.most_common()))

succ = [i for i in ua if i in VERDICT and VERDICT[i][0]=='false' and ua[i]['status']=='succeeded']
print("\nDANGEROUS false+succeeded after:", len(succ), succ)

# per-category coverage
cats = collections.defaultdict(lambda: [0,0])
for i, r in after.items():
    cats[r['cat']][1] += 1
    if r['understood']:
        cats[r['cat']][0] += 1
print("\ncoverage by category (after):")
for k in sorted(cats):
    print("  %-16s %d/%d" % (k, cats[k][0], cats[k][1]))
