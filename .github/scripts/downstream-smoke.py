"""Read-only CLI proof; invoked inside downstream-compat.sh's PID/network namespace."""

import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys


SMOKES = {
    "help": ["--help"],
    "version": ["--version"],
    "tui-help": ["tui", "--help"],
    "metadata": ["metadata", "--json"],
    "status": ["status", "--json"],
    "tui": ["tui", "--json"],
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def snapshot(root):
    result = {}
    for path in sorted(root.rglob("*")):
        mode = path.lstat().st_mode
        value = {"mode": mode}
        if stat.S_ISLNK(mode):
            value["target"] = os.readlink(path)
        elif stat.S_ISREG(mode):
            value["sha256"] = digest(path.read_bytes())
        elif not stat.S_ISDIR(mode):
            raise ValueError(f"unexpected runtime file type: {path}")
        result[str(path.relative_to(root))] = value
    return result


def normalized(value, runtime):
    if isinstance(value, str):
        if value == runtime or value.startswith(runtime + "/"):
            return "<runtime>" + value[len(runtime):]
        return value
    if isinstance(value, list):
        return [normalized(item, runtime) for item in value]
    if isinstance(value, dict):
        return {key: normalized(item, runtime) for key, item in value.items()}
    return value


def comparable(result):
    assert result["passed"], "cannot compare an incomplete or failing smoke"
    values = normalized(result["json"], result["runtime"])
    # control.NewStatus records wall time; no other field may be ignored.
    timestamp = values["status"]["generated_at"]
    assert isinstance(timestamp, str) and timestamp, "missing status timestamp"
    values["status"]["generated_at"] = "<generated-at>"
    return values


def run(app, binary, runtime, evidence):
    assert app in ("slacrawl", "discrawl", "notcrawl"), "unsupported app"
    runtime, evidence = Path(runtime), Path(evidence)
    evidence.mkdir()
    result = {"app": app, "runtime": str(runtime), "steps": [], "json": {}, "passed": False}
    prefix = [binary, "--config", str(runtime / "config" / (app + ".toml"))]

    def command(name, args):
        argv = prefix + args
        print(json.dumps({"command": argv}), flush=True)
        completed = subprocess.run(argv, cwd=runtime, input=b"", capture_output=True, timeout=30)
        step = {"name": name, "argv": argv, "exit": completed.returncode}
        for stream in ("stdout", "stderr"):
            data = getattr(completed, stream)
            (evidence / (name + "." + stream)).write_bytes(data)
            step[stream + "_sha256"] = digest(data)
            print(data.decode("utf-8", errors="replace"), end="", flush=True)
        result["steps"].append(step)
        assert completed.returncode == 0, f"{name} exited {completed.returncode}"
        return completed.stdout.decode("utf-8")

    try:
        if app == "slacrawl":
            command("fixture-init", ["init", "--db", str(runtime / "runtime" / "archive.db")])
        elif app == "notcrawl":
            command("fixture-init", ["init"])
        else:
            print("Discrawl: native absent-config/archive path; no authenticated init", flush=True)
        result["before"] = snapshot(runtime)
        for name, args in SMOKES.items():
            text = command(name, args)
            if name in ("metadata", "status", "tui"):
                result["json"][name] = json.loads(text)
            else:
                assert text.strip(), f"{name} returned empty output"
        result["after"] = snapshot(runtime)
        assert result["before"] == result["after"], "read-only smokes changed runtime files"
        result["passed"] = True
    finally:
        data = (json.dumps(result, indent=2, sort_keys=True) + "\n").encode()
        (evidence / "result.json").write_bytes(data)
        print(json.dumps({"smoke_result": result, "sha256": digest(data)}), flush=True)


if __name__ == "__main__":
    if len(sys.argv) == 6 and sys.argv[1] == "run":
        run(*sys.argv[2:])
    elif len(sys.argv) == 4 and sys.argv[1] == "compare":
        baseline, candidate = [json.loads(Path(name).read_text()) for name in sys.argv[2:]]
        assert baseline["app"] == candidate["app"], "app identity mismatch"
        assert comparable(baseline) == comparable(candidate), "baseline/candidate JSON differs"
        print("baseline/candidate metadata, status, and TUI JSON match")
    else:
        sys.exit("usage: downstream-smoke.py run APP BINARY RUNTIME EVIDENCE | compare BASELINE CANDIDATE")
