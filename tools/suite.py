#!/usr/bin/env python3
"""Run a jevify choice suite (JSONL: instruction, context, options, label, case_id, group)
against /v1/systemone and print accuracy per group.

usage: suite.py SUITE.jsonl [--url URL] [--model ID] [-c N] [--group G ...] [--limit N] [--bodies]

--bodies prints the request bodies as a JSON array and sends nothing.
Set API_KEY if the server has one. Suites: https://github.com/fidecastro/jevify/tree/main/suites
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor


def body(row, model):
    # option text is the choice key (value null), in order: key order fixes the letters the model sees
    return {
        "model": model,
        "state": row["context"],
        "questions": {"q": {"type": "choice", "instructions": row["instruction"],
                            "criteria": {o: None for o in row["options"]}}},
    }


def send(url, b):
    headers = {"Content-Type": "application/json"}
    if os.environ.get("API_KEY"):
        headers["Authorization"] = "Bearer " + os.environ["API_KEY"]
    req = urllib.request.Request(url, json.dumps(b).encode(), headers)
    t = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            return json.load(r)["answers"]["q"], time.perf_counter() - t
    except urllib.error.HTTPError as e:
        return {"error": f"{e.code} {e.read()[:120].decode(errors='replace')}"}, 0
    except (urllib.error.URLError, OSError) as e:  # refused, reset, timed out: one case must not lose the run
        return {"error": str(e)[:120]}, 0


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("suite")
    p.add_argument("--url", default="http://localhost:8080/v1/systemone")
    p.add_argument("--model", default="jev-latest")
    p.add_argument("-c", type=int, default=1, help="requests in flight (server runs -slots at a time)")
    p.add_argument("--group", action="append", help="only these groups")
    p.add_argument("--limit", type=int)
    p.add_argument("--bodies", action="store_true")
    a = p.parse_args()

    rows = [json.loads(l) for l in open(a.suite) if l.strip()]
    if a.group:
        rows = [r for r in rows if r.get("group") in a.group]
    rows = rows[: a.limit]
    for r in rows:  # duplicate options would collapse into one criteria key
        if len(set(r["options"])) != len(r["options"]):
            sys.exit(f"{r['case_id']}: duplicate option texts")
    bodies = [body(r, a.model) for r in rows]
    if a.bodies:
        return print(json.dumps(bodies, indent=2, ensure_ascii=False))

    with ThreadPoolExecutor(a.c) as ex:
        results = list(ex.map(lambda b: send(a.url, b), bodies))

    stats = defaultdict(lambda: [0, 0, 0])  # group -> [right, total, errors]
    ms = []
    for r, (ans, secs) in zip(rows, results):
        s = stats[r.get("group", "all")]
        s[1] += 1
        if "error" in ans:
            s[2] += 1
            print(f"ERR  {r['case_id']:<22} {ans['error']}")
            continue
        ms.append(secs * 1000)
        want = r["options"][r["label"]]
        if ans["choice"] == want:
            s[0] += 1
        else:
            print(f"FAIL {r['case_id']:<22} conf {ans['confidence']:.2f}  got {ans['choice']!r}  want {want!r}")

    print()
    for g, (ok, n, err) in stats.items():
        print(f"{g:<14} {ok}/{n}" + (f"  ({err} errors)" if err else ""))
    ok, n, err = (sum(s[i] for s in stats.values()) for i in range(3))
    print(f"{'total':<14} {ok}/{n} ({100 * ok // max(n, 1)}%)" + (f"  {err} errors" if err else ""))
    if ms:
        print(f"latency        median {sorted(ms)[len(ms) // 2]:.0f} ms, max {max(ms):.0f} ms")
    sys.exit(ok != n)


main()
