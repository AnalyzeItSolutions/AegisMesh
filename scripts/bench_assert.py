#!/usr/bin/env python3
"""Run a benchmark command and assert its number against a target. Standard library only; copied into the project by the builder.

    bench_assert.py NAME OP VALUE --cmd "make bench-x" [--unit ms]
The command must print one line `BENCH <NAME> <number>` (anything else it prints is ignored). OP is < <= > >=. Exit 0 = the target holds,
1 = it does not, 2 = the command did not print the line (a harness that measures nothing is a failure, not a pass).
"""
import argparse
import operator
import re
import subprocess
import sys

OPS = {"<": operator.lt, "<=": operator.le, ">": operator.gt, ">=": operator.ge}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("name"); ap.add_argument("op", choices=list(OPS)); ap.add_argument("value", type=float)
    ap.add_argument("--cmd", required=True); ap.add_argument("--unit", default=""); ap.add_argument("--timeout", type=int, default=900)
    a = ap.parse_args()
    p = subprocess.run(["bash", "-lc", a.cmd], capture_output=True, text=True, timeout=a.timeout)
    out = p.stdout + p.stderr
    found = re.findall(rf"^BENCH\s+{re.escape(a.name)}\s+(-?\d+(?:\.\d+)?)", out, re.M)
    if p.returncode != 0:
        print(out[-1500:]); print(f"BENCH FAILED: `{a.cmd}` exited {p.returncode}"); return 1
    if not found:
        print(out[-800:]); print(f"BENCH MISSING: `{a.cmd}` printed no line `BENCH {a.name} <number>`"); return 2
    got = float(found[-1])
    ok = OPS[a.op](got, a.value)
    print(f"BENCH {a.name}: measured {got:g}{a.unit} (target {a.op} {a.value:g}{a.unit}) -> {'OK' if ok else 'MISSED'}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
