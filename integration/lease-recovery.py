#!/usr/bin/env python3
"""Exercise awaiting/replacement ownership through real vb and hvb, stub Herdr."""
import json
import os
from pathlib import Path
import subprocess
import sys

root = Path(sys.argv[1])
stubs = Path(sys.argv[2])
vb = os.environ["VB_REAL_BIN"]
hvb = os.environ["HVB_BIN"]
assert os.environ["HERDR_BIN_PATH"] == str(stubs / "herdr")
assert os.environ["HVB_VB_BIN"] == vb


def invoke(binary, arguments, expected=0):
    result = subprocess.run([binary, "--root", str(root), "--json", *arguments], capture_output=True, text=True)
    assert result.returncode == expected, f"{Path(binary).name} {arguments[0]}: exit {result.returncode}, expected {expected}"
    return json.loads(result.stdout) if result.stdout.strip() else None


def create(title):
    feature = invoke(vb, ["new", title, "--template", "light"])["data"]["id"]
    invoke(vb, ["move", feature, "in-progress"])
    return feature


feature = create("Awaiting human verification")
started = invoke(hvb, ["run", "start", feature])
assert started["lock_token"] and started["state"] == "running"
(stubs / "live_panes").write_text("")
awaiting = invoke(hvb, ["run", "show", started["id"]])
assert awaiting["state"] == "awaiting" and awaiting["lease_released"]
assert invoke(vb, ["lock", feature, "--status"])["data"]["locked"] is False
# A human report must wait for another owner's lease, rather than forcing it.
other = invoke(vb, ["lock", feature, "--owner", "bob"])["data"]
invoke(hvb, ["run", "done", started["id"], "--outcome", "success"], expected=5)
current = invoke(vb, ["lock", feature, "--status"])["data"]
assert current["token"] == other["token"] and current["owner"] == "bob"
blocked = invoke(hvb, ["run", "show", started["id"]])
assert blocked["state"] == "awaiting" and not blocked.get("completion"), "refused report changed the awaiting run"
invoke(vb, ["--actor", "bob", "lock", feature, "--release", "--token", other["token"]])
completed = invoke(hvb, ["run", "done", started["id"], "--outcome", "success", "--note", "human verified the change"])
assert completed["run"]["completion"]["done"] and completed["run"]["lease_released"]
assert completed["run"]["lock_token"] != started["lock_token"], "human outcome reused a released nonce"
assert completed["run"]["moved_to"] == "review"
assert list((root / ".virtualboard/features/review").glob(feature + "*.md"))
assert invoke(vb, ["lock", feature, "--status"])["data"]["locked"] is False

feature = create("Successor ownership preservation")
started = invoke(hvb, ["run", "start", feature])
successor = invoke(vb, ["lock", feature, "--owner", "bob", "--force"])["data"]
assert successor["token"] != started["lock_token"]
cancelled = invoke(hvb, ["run", "cancel", started["id"]])
assert cancelled["state"] == "cancelled" and cancelled["lease_released"]
current = invoke(vb, ["lock", feature, "--status"])["data"]
assert current["owner"] == "bob" and current["token"] == successor["token"], "cancellation removed the successor lease"
invoke(vb, ["--actor", "bob", "lock", feature, "--release", "--token", successor["token"]])
print("real lease recovery: awaiting release, fresh human-outcome ownership, routing, and successor-safe cancellation passed")
