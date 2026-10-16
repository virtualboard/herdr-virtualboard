#!/usr/bin/env python3
"""Check shared bytes and enforce every lifecycle edge through both real CLIs."""
import json
import os
from pathlib import Path
import subprocess
import sys

project = Path(sys.argv[1])
template = Path(sys.argv[2])
hvb_repo = Path(__file__).resolve().parents[1]
raw = (template / "schemas/workflow-contract.json").read_bytes()
for repo in (hvb_repo, Path(os.environ.get("VB_SOURCE", str(hvb_repo.parent / "vb-cli")))):
    assert (repo / "testdata/workflow-contract.json").read_bytes() == raw, f"contract drift in {repo}"
contract = json.loads(raw)
assert contract["format_version"] == 1
vb = os.environ["VB_REAL_BIN"]
hvb = os.environ["HVB_BIN"]


def invoke(binary, arguments, expected=0):
    result = subprocess.run([binary, "--root", str(project), "--json", *arguments], capture_output=True, text=True)
    assert result.returncode == expected, f"{Path(binary).name} {arguments}: exit {result.returncode}, expected {expected}: {result.stdout} {result.stderr}"
    return result.stdout


report = json.loads(invoke(vb, ["doctor", "--capabilities-only"]))["data"]
assert report["format_version"] == contract["doctor"]["format_version"]
assert report["capabilities"] == contract["doctor"]["vb_capabilities"]
setup_routes = {
    "backlog": [],
    "in-progress": ["in-progress"],
    "blocked": ["in-progress", "blocked"],
    "review": ["in-progress", "review"],
    "done": ["in-progress", "review", "done"],
}
count = 0
for tool, binary, prefix in (("vb", vb, []), ("hvb", hvb, ["feature"])):
    for origin in contract["statuses"]:
        for target in contract["statuses"]:
            feature = json.loads(invoke(vb, ["new", f"Contract {origin} to {target}", "--template", "light"]))["data"]["id"]
            for status in setup_routes[origin]:
                invoke(vb, ["move", feature, status])
            source = next((project / ".virtualboard/features" / origin).glob(feature + "*.md"))
            before = source.read_bytes()
            allowed = target in contract["transitions"][origin]
            invoke(binary, [*prefix, "move", feature, target], 0 if allowed else contract[tool]["lifecycle_refusal_exit"])
            if allowed:
                assert list((project / ".virtualboard/features" / target).glob(feature + "*.md")), f"move did not reach {target}"
            else:
                assert source.read_bytes() == before, f"forbidden transition changed {feature}"
            count += 1
print(f"workflow contract: {count} real CLI transitions, shared fixture bytes, and runtime capabilities passed")
