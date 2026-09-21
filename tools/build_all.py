#!/usr/bin/env python3
"""Build testdata/test.all.json (one request, every question) from testdata/test.json.

The endpoint takes one state per request, so the messages go under state.messages[<id>]
and each instruction is prefixed with where to find its message.
"""
import json
import pathlib

d = pathlib.Path(__file__).resolve().parent.parent / "testdata"
msgs, qs = {}, {}
for r in json.load(open(d / "test.json")):
    (id, q), = r["questions"].items()
    assert isinstance(q["instructions"], str), id
    msgs[id] = r["state"]
    q["instructions"] = f'About the message at state.messages["{id}"]: {q["instructions"]}'
    qs[id] = q
body = {"model": "jev-latest", "state": {"messages": msgs}, "questions": qs}
json.dump(body, open(d / "test.all.json", "w"), indent=2, ensure_ascii=False)
print(len(qs), "questions ->", d / "test.all.json")
