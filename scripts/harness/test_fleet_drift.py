#!/usr/bin/env python3
"""Tests for fleet_drift.py. Run: python3 scripts/harness/test_fleet_drift.py

fleet_drift.py reads the canonical rule sets from IQVO/warehouse-docs via `gh api`; these
tests put a fake `gh` on PATH that serves canned contents-API responses from a fixture file
(GH_FIXTURE env var), so no network or token is needed. Fixture repos live in temp dirs.
"""
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest

DRIFT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fleet_drift.py")

FAKE_GH = """#!/usr/bin/env python3
import base64, json, os, sys
fixture = json.load(open(os.environ["GH_FIXTURE"]))
arg = sys.argv[2]  # repos/<owner>/<repo>/contents/<path>?ref=main
path = arg.split("/contents/", 1)[1].split("?", 1)[0]
if path not in fixture:
    print(f"gh: {path}: Not Found (HTTP 404)", file=sys.stderr)
    sys.exit(1)
entry = fixture[path]
if isinstance(entry, dict):
    print(json.dumps([{"name": n, "path": f"{path}/{n}"} for n in entry]))
else:
    print(json.dumps({"name": os.path.basename(path), "path": path,
                      "content": base64.b64encode(entry.encode()).decode()}))
"""

FLEET = {
    "cloudevents.md": "# CloudEvents\nwarehouse variant: com.warehouse.<subdomain>.<context>\n",
    "domain-model.md": "# Domain model\nfleet rule\n",
    "rest-api.md": "# REST API\nfleet rule\n",
}
RETAILNETWORK = {
    "cloudevents.md": "# CloudEvents\nnetwork variant: com.retailnetwork.<tier>.<context>\n",
    "domain-model.md": "# Domain model\nfleet rule\n",
    "rest-api.md": "# REST API\nfleet rule\n",
}


def make_fixture(retailnetwork_published=True):
    """-> fixture path mapping contents-API paths to file content or a directory listing."""
    mapping = {}
    sets = {"agents/fleet": FLEET}
    if retailnetwork_published:
        sets["agents/retailnetwork"] = RETAILNETWORK
    for prefix, files in sets.items():
        mapping[prefix] = {name: None for name in files}  # dict => directory listing
        for name, content in files.items():
            mapping[f"{prefix}/{name}"] = content
    fd, path = tempfile.mkstemp(prefix="fd-fixture-", suffix=".json")
    with os.fdopen(fd, "w") as f:
        json.dump(mapping, f)
    return path


def make_repo(files):
    """-> repo root with .claude/rules/fleet/<name> for each files entry."""
    root = tempfile.mkdtemp(prefix="fd-repo-")
    d = os.path.join(root, ".claude", "rules", "fleet")
    os.makedirs(d, exist_ok=True)
    for name, content in files.items():
        with open(os.path.join(d, name), "w") as f:
            f.write(content)
    return root


class FleetDrift(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bindir = tempfile.mkdtemp(prefix="fd-bin-")
        gh = os.path.join(cls.bindir, "gh")
        with open(gh, "w") as f:
            f.write(FAKE_GH)
        os.chmod(gh, os.stat(gh).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    def drift(self, repo_root, retailnetwork_published=True):
        env = dict(os.environ)
        env["PATH"] = self.bindir + os.pathsep + env["PATH"]
        env["GH_FIXTURE"] = make_fixture(retailnetwork_published)
        return subprocess.run([sys.executable, DRIFT, "--repo-root", repo_root],
                              capture_output=True, text=True, env=env)

    def test_warehouse_repo_in_sync(self):
        r = self.drift(make_repo(FLEET))
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("agents/fleet", r.stdout)

    def test_retailnetwork_repo_in_sync(self):
        r = self.drift(make_repo(RETAILNETWORK))
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("agents/retailnetwork", r.stdout)

    def test_retailnetwork_repo_canonical_set_not_published(self):
        r = self.drift(make_repo(RETAILNETWORK), retailnetwork_published=False)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("agents/retailnetwork not published yet", r.stdout)
        self.assertIn("FIX:", r.stdout)

    def test_warehouse_repo_hand_edit_is_drift(self):
        repo = make_repo({**FLEET, "rest-api.md": "# REST API\nhand-edited locally\n"})
        r = self.drift(repo)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("rest-api.md: differs", r.stdout)

    def test_drifting_repo_reported_against_closer_set(self):
        # cloudevents.md matches neither set; the other files match fleet, so fleet is closer.
        repo = make_repo({**FLEET, "cloudevents.md": "# CloudEvents\nsomething else entirely\n"})
        r = self.drift(repo)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("matches no canonical set", r.stdout)
        self.assertIn("agents/fleet", r.stdout)
        self.assertIn("cloudevents.md: differs", r.stdout)

    def test_extra_local_file_is_drift(self):
        repo = make_repo({**FLEET, "extra-rule.md": "# Not in the canonical set\n"})
        r = self.drift(repo)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("extra-rule.md: not in canonical set", r.stdout)

    def test_missing_local_file_is_drift(self):
        repo = make_repo({k: v for k, v in FLEET.items() if k != "rest-api.md"})
        r = self.drift(repo)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("rest-api.md: missing locally", r.stdout)

    def test_no_fleet_dir_nothing_to_check(self):
        root = tempfile.mkdtemp(prefix="fd-empty-")
        r = self.drift(root)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nothing to check", r.stdout)


if __name__ == "__main__":
    unittest.main()
