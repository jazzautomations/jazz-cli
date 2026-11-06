#!/usr/bin/env python3
"""Analyze results.jsonl: coverage by category, uncovered classes, false-match
candidates. Every number comes from the recorded runs."""
import json, os, collections, re, sys

import os
RES = os.environ.get('BENCH_OUT', os.environ.get('JAZZ_WORK','/tmp/v5-work') + '/results.jsonl')
rows = [json.loads(l) for l in open(RES, encoding='utf-8')]
print("total results:", len(rows))

# ---- per-category coverage ------------------------------------------------
cats = collections.defaultdict(lambda: {"n": 0, "ok": 0, "intents": collections.Counter(),
                                        "status": collections.Counter(), "rows": []})
for r in rows:
    c = cats[r['cat']]
    c['n'] += 1
    c['ok'] += 1 if r['understood'] else 0
    c['status'][r['status']] += 1
    if r['intent']:
        c['intents'][r['intent']] += 1
    c['rows'].append(r)

print("\n== coverage by category ==")
grand_n = grand_ok = 0
for k in sorted(cats):
    c = cats[k]
    grand_n += c['n']; grand_ok += c['ok']
    pct = 100.0 * c['ok'] / c['n']
    print(f"{k:16s} {c['ok']:3d}/{c['n']:3d}  {pct:5.1f}%   intents={dict(c['intents'].most_common(4))}")
print(f"{'TOTAL':16s} {grand_ok:3d}/{grand_n:3d}  {100.0*grand_ok/grand_n:5.1f}%")

# by source
srcs = collections.defaultdict(lambda: [0, 0])
langs = collections.defaultdict(lambda: [0, 0])
for r in rows:
    srcs[r['src']][0] += 1 if r['understood'] else 0
    srcs[r['src']][1] += 1
    langs[r['lang']][0] += 1 if r['understood'] else 0
    langs[r['lang']][1] += 1
print("\n== by source ==")
for k, (o, n) in sorted(srcs.items()):
    print(f"{k:10s} {o}/{n} {100.0*o/n:.1f}%")
print("== by lang ==")
for k, (o, n) in sorted(langs.items()):
    print(f"{k:6s} {o}/{n} {100.0*o/n:.1f}%")

# ---- understood rows: list intent vs request for false-match review -------
print("\n== understood rows (intent <- request) for manual false-match audit ==")
for r in rows:
    if r['understood']:
        print(f"{r['id']:7s} [{r['cat']}] {r['intent']:22s} st={r['status']:<14s} | {r['req'][:110]}")

# ---- missed rows grouped by rough request class ---------------------------
print("\n== missed (not understood) by category ==")
miss = [r for r in rows if not r['understood']]
mc = collections.Counter(r['cat'] for r in miss)
for k, v in mc.most_common():
    print(f"  {k}: {v}")

print("\n== missed rows with guess ==")
for r in miss:
    g = f" guess={r['guess']}" if r['guess'] else ""
    print(f"{r['id']:7s} [{r['cat']}]{g} | {r['req'][:110]}")
