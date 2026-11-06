#!/usr/bin/env python3
"""Tests for guide_lint.py's template mode: in the template repository the guides describe the future
service, so service-tree paths, CLAUDE.md/AGENTS.md and Makefile targets are not checked there; in a
service they still are. Run: python3 scripts/harness/test_guide_lint.py"""
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
LINT = os.path.join(HERE, "guide_lint.py")
RULE = ("# rule\n\nUse cases live in `internal/application/usecases`; see `CLAUDE.md`, run `make mutation-fast`.\n"
        "The hook is `scripts/harness/missing_hook.py`.\n")


def run(template: bool) -> str:
    root = tempfile.mkdtemp()
    os.makedirs(os.path.join(root, ".claude", "rules"))
    os.makedirs(os.path.join(root, ".github", "workflows"))
    with open(os.path.join(root, ".claude", "rules", "r.md"), "w") as fh:
        fh.write(RULE)
    with open(os.path.join(root, "Makefile"), "w") as fh:
        fh.write("check:\n\ttrue\n")
    wf = "ci.yml.template" if template else "ci.yml"
    with open(os.path.join(root, ".github", "workflows", wf), "w") as fh:
        fh.write("name: CI\n")
    p = subprocess.run([sys.executable, LINT, "--root", root], capture_output=True, text=True)
    return p.stdout


def main() -> int:
    failures = []
    t = run(template=True)
    for needle in ("internal/application/usecases", "`CLAUDE.md`", "make mutation-fast"):
        if needle in t:
            failures.append(f"template mode must not flag {needle}:\n{t}")
    if "scripts/harness/missing_hook.py" not in t:
        failures.append(f"template mode must still flag a missing template path (scripts/...):\n{t}")
    s = run(template=False)
    for needle in ("internal/application/usecases", "`CLAUDE.md`", "make mutation-fast", "scripts/harness/missing_hook.py"):
        if needle not in s:
            failures.append(f"service mode must flag {needle}:\n{s}")
    for f in failures:
        print("FAIL:", f)
    print("test_guide_lint:", "FAILED" if failures else "ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
