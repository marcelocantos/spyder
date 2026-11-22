"""Condense a TLC counterexample: one line per step, the action and the
variables it changed (value text squeezed onto one line).
Usage: python3 -I trace.py <tlc-output-file>"""
import re, sys

text = open(sys.argv[1]).read()
if "The behavior up to this point is:" not in text:
    sys.exit("no counterexample")
text = text.split("The behavior up to this point is:", 1)[1]
states = re.split(r"\nState (\d+): ", text)[1:]
prev = {}
for i in range(0, len(states), 2):
    num, body = states[i], states[i + 1]
    body = body.split("\n\n", 1)[0]
    head, _, rest = body.partition("\n")
    action = re.sub(r" line .*", "", head).strip("<> ")
    cur = {}
    for m in re.finditer(r"^/\\ (\w+) = (.*?)(?=^/\\ |\Z)", rest, re.S | re.M):
        cur[m.group(1)] = " ".join(m.group(2).split())
    changed = {k: v for k, v in cur.items() if prev.get(k) != v}
    if num == "1":
        changed = {k: cur[k] for k in ("book", "pc", "present", "daemonUp") if k in cur}
    print(f"{num:>3} {action}")
    for k, v in changed.items():
        v = re.sub(r'\[owner \|-> "none", rem \|-> 0\]', "-", v)
        print(f"      {k} = {v}")
    prev = cur
    if "Error:" in body or "Finished" in body:
        break
