#!/usr/bin/env python3
"""Static checks for generated files: sql, k8s, compose. Standard library only; copied into the project by the builder.

    check_generated.py sql db/migrations | k8s infra/k8s | files PATH...
"""
import os
import re
import sys


def files_under(d, ext):
    return sorted(os.path.join(r, f) for r, _, fs in os.walk(d) for f in fs if f.endswith(ext))


def check_sql(d):
    bad = []
    fs = files_under(d, ".sql")
    if not fs:
        return [f"no .sql files under {d}"]
    for p in fs:
        t = open(p).read()
        code = re.sub(r"\$\$.*?\$\$", "", re.sub(r"--[^\n]*", "", t), flags=re.S)
        if code.count("(") != code.count(")"):
            bad.append(f"{p}: unbalanced parentheses")
        if code.strip() and not code.strip().endswith(";"):
            bad.append(f"{p}: last statement has no semicolon")
        for m in re.finditer(r"create table(?: if not exists)? (\w+)\s*\((.*?)\n\);", t, re.S | re.I):
            if "tenant_id" in m.group(2) and not re.search(rf"alter table {m.group(1)} enable row level security", t, re.I):
                bad.append(f"{p}: table {m.group(1)} has tenant_id but no row level security")
    return bad


def check_k8s(d):
    bad = []
    fs = files_under(d, ".yaml") + files_under(d, ".yml")
    if not fs:
        return [f"no yaml files under {d}"]
    for p in fs:
        for doc in re.split(r"^---\s*$", open(p).read(), flags=re.M):
            if doc.strip() and not (re.search(r"^apiVersion:", doc, re.M) and re.search(r"^kind:", doc, re.M)):
                bad.append(f"{p}: a document lacks apiVersion or kind")
    return bad


def main():
    mode, args = sys.argv[1], sys.argv[2:]
    if mode == "sql": bad = check_sql(args[0])
    elif mode == "k8s": bad = check_k8s(args[0])
    elif mode == "files": bad = [f"missing: {p}" for p in args if not os.path.exists(p)]
    else: bad = [f"unknown mode {mode}"]
    for b in bad: print(b)
    print("OK" if not bad else f"{len(bad)} problem(s)")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
