#!/usr/bin/env python3
"""Score /v1/systemone responses against test.expected.json.

usage: score.py RESULTS.json [EXPECTED.json]     (RESULTS "-" = stdin; default expected: testdata/test.expected.json)

RESULTS is one response, or a JSON array of responses (one per request).
noul: > 0.5 is "true". choice: the chosen key. score: round-half-up of the weighted level.
"""
import json
import pathlib
import sys


def pick(a):
    if a["type"] == "noul":
        return str(a["noul"] > 0.5).lower()
    if a["type"] == "choice":
        return a["choice"]
    return str(int(a["score"] + 0.5))  # ponytail: half-up, so 3.5 -> 4


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    src = sys.stdin if sys.argv[1] == "-" else open(sys.argv[1])
    res = json.load(src)
    default = pathlib.Path(__file__).resolve().parent.parent / "testdata/test.expected.json"
    exp = json.load(open(sys.argv[2] if len(sys.argv) > 2 else default))
    answers = {}
    for r in res if isinstance(res, list) else [res]:
        if "answers" not in r:  # an error response: report it, the ids show up as MISSING
            print("ERROR RESPONSE:", json.dumps(r.get("detail", r))[:200])
        answers.update(r.get("answers", {}))

    fails = 0
    for k in sorted(exp):
        a = answers.get(k)
        got = pick(a) if a else "MISSING"
        if got != exp[k]:
            fails += 1
            print(f"FAIL {k:<22} got {got:<22} want {exp[k]}")
    n = len(exp)
    print(f"{n - fails}/{n} passed ({100 * (n - fails) // n}%)")
    extra = set(answers) - set(exp)
    if extra:
        print("no expected value for:", ", ".join(sorted(extra)))
    sys.exit(fails > 0)


main()
