"""Puts the generated stubs (gen/py) and this service's directory on sys.path so `server`, `routing_vrp` and `geo_h3` import from anywhere."""
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
for p in (HERE, HERE.parent.parent / "gen" / "py", HERE / "routing_vrp", HERE / "geo_h3"):
    if str(p) not in sys.path:
        sys.path.insert(0, str(p))
