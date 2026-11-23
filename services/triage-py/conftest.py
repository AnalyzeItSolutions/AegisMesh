"""Puts the generated stubs (gen/py) and this service's directory on sys.path so `server`, `dedupe`, `start_tiers` and `token_bucket` import from anywhere."""
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
for p in (HERE, HERE.parent.parent / "gen" / "py", HERE / "priority", HERE / "ratelimit"):
    if str(p) not in sys.path:
        sys.path.insert(0, str(p))
