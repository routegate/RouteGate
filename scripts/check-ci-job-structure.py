#!/usr/bin/env python3
"""Reject repeated top-level GitHub Actions jobs without third-party YAML parsers.

This deliberately checks only the structural regression that previously
duplicated the entire RouteGate CI jobs section. It is not a YAML validator.
"""
from pathlib import Path
import re
import sys

workflow = Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml"
lines = workflow.read_text(encoding="utf-8").splitlines()
jobs_headers = [i for i, line in enumerate(lines) if line == "jobs:"]
if len(jobs_headers) != 1:
    sys.exit(f"expected exactly one top-level jobs: section; found {len(jobs_headers)}")

jobs = {}
for number, line in enumerate(lines[jobs_headers[0] + 1:], start=jobs_headers[0] + 2):
    # Top-level YAML keys end the jobs mapping; jobs use exactly two spaces.
    if line and not line[0].isspace() and not line.startswith("#"):
        break
    match = re.fullmatch(r"  ([a-zA-Z][a-zA-Z0-9_-]*):(?:\s*#.*)?", line)
    if match:
        name = match.group(1)
        if name in jobs:
            sys.exit(f"duplicate CI job {name!r} at lines {jobs[name]} and {number}")
        jobs[name] = number

expected = {"backend", "agent", "installer", "remote-node-bootstrap", "frontend", "website"}
missing = expected - jobs.keys()
if missing:
    sys.exit(f"missing CI jobs: {', '.join(sorted(missing))}")
print(f"CI job structure OK: {len(jobs)} distinct jobs")
