const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');

function python(code) {
  const result = spawnSync('python3', ['-B', '-c', `
import importlib.util
import json
from pathlib import Path
import tempfile
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("smoke", "downstream-smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
${code}
`], { cwd: __dirname, encoding: 'utf8', timeout: 10000 });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr || result.stdout);
}

test('manual qualification selects one pinned app without repeating ordinary CI', () => {
  const workflow = readFileSync(`${__dirname}/../workflows/ci.yml`, 'utf8');
  assert.match(workflow, /options: \[slacrawl, discrawl, notcrawl\]/);
  for (const job of ['test', 'downstream', 'windows-test']) {
    assert.ok(workflow.includes(`  ${job}:\n    if: github.event_name != 'workflow_dispatch'\n`));
  }
  assert.match(workflow, /  downstream-app:\n    if: github.event_name == 'workflow_dispatch'\n    runs-on: ubuntu-latest\n    timeout-minutes: 20/);
  assert.match(workflow, /group: crawlkit-downstream-manual\n      cancel-in-progress: false/);
  for (const sha of [
    'ffaff3a72d91c5a1c2c6bf882412ef6e73e6dc78',
    '9e8a981f39563aa73fb096eb903e801ac625fb84',
    '5b2af061b057618eee38e322d801d54ca9ffba36',
  ]) assert.ok(workflow.includes(sha));
});

test('normalization preserves values except runtime paths and the top-level status clock', () => {
  python(`
original = {
    "passed": True, "runtime": "/proof/baseline",
    "json": {
        "metadata": {"path": "/proof/baseline/home", "text": "prefix /proof/baseline"},
        "status": {"generated_at": "2026-09-09T00:00:00Z", "nested": {"generated_at": "retained"}},
        "tui": [{"id": 9007199254740993, "path": "/proof/baseline-other"}],
    },
}
value = smoke.comparable(original)
assert value["metadata"] == {"path": "<runtime>/home", "text": "prefix /proof/baseline"}
assert value["status"] == {"generated_at": "<generated-at>", "nested": {"generated_at": "retained"}}
assert value["tui"] == original["json"]["tui"]
assert original["json"]["status"]["generated_at"] == "2026-09-09T00:00:00Z"
original["passed"] = False
try:
    smoke.comparable(original)
except AssertionError:
    pass
else:
    raise AssertionError("a failed phase was accepted")
`);
});

test('runtime snapshots detect content, mode, and symlink changes', () => {
  python(`
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    file = root / "archive.db"
    file.write_bytes(b"before")
    before = smoke.snapshot(root)
    file.write_bytes(b"after")
    assert smoke.snapshot(root) != before
    before = smoke.snapshot(root)
    file.chmod(0o400)
    assert smoke.snapshot(root) != before
    before = smoke.snapshot(root)
    (root / "link").symlink_to("archive.db")
    assert smoke.snapshot(root) != before
`);
});

for (const app of ['slacrawl', 'discrawl', 'notcrawl']) {
  test(`${app} smoke contract uses six readonly commands and only its native local fixture`, () => {
    python(`
from types import SimpleNamespace
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    runtime = root / "runtime"
    runtime.mkdir()
    calls = []
    def command(argv, **kwargs):
        assert kwargs["timeout"] == 30 and kwargs["input"] == b""
        assert kwargs["cwd"] == runtime
        calls.append(argv[3:])
        name = argv[3]
        if name == "init":
            (runtime / "fixture").write_text("local init")
        text = '{"generated_at":"2026-09-09T00:00:00Z"}' if name == "status" else '{}'
        return SimpleNamespace(returncode=0, stdout=text.encode(), stderr=b"")
    with patch.object(smoke.subprocess, "run", command):
        smoke.run("${app}", "/test/bin/${app}", str(runtime), str(root / "evidence"))
    expected = list(smoke.SMOKES.values())
    if "${app}" == "slacrawl":
        expected.insert(0, ["init", "--db", str(runtime / "runtime" / "archive.db")])
    elif "${app}" == "notcrawl":
        expected.insert(0, ["init"])
    assert calls == expected
    result = json.loads((root / "evidence/result.json").read_text())
    assert result["passed"] and result["before"] == result["after"]
    assert len(list((root / "evidence").glob("*.stdout"))) == len(expected)
`);
  });
}

test('failed commands and runtime writes leave a failed receipt, never a pass', () => {
  python(`
from types import SimpleNamespace
for failure in ("exit", "mutation", "empty", "json"):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        runtime = root / "runtime"
        runtime.mkdir()
        def command(argv, **kwargs):
            if failure == "mutation":
                (runtime / "unexpected").write_text("write")
            text = b"" if failure == "empty" else b"{}"
            if failure == "json" and argv[3] == "metadata":
                text = b"invalid"
            return SimpleNamespace(returncode=1 if failure == "exit" else 0, stdout=text, stderr=b"")
        with patch.object(smoke.subprocess, "run", command):
            try:
                smoke.run("discrawl", "/test/bin/discrawl", str(runtime), str(root / "evidence"))
            except (AssertionError, ValueError):
                pass
            else:
                raise AssertionError(f"{failure} was accepted")
        result = json.loads((root / "evidence/result.json").read_text())
        assert not result["passed"]
`);
});
