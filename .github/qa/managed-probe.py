"""Build real sibling binaries and exercise the private no-acquisition probe."""
import os
import subprocess
import tempfile
from pathlib import Path

workspace = Path.cwd()
env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off")
suffix = ".exe" if os.name == "nt" else ""
with tempfile.TemporaryDirectory(prefix="mebooru-probe-build-") as scratch:
    directory = Path(scratch)
    library = directory / ("monbooru" + suffix)
    loader = directory / ("monloader" + suffix)
    for repo, name, binary in [(workspace, "monbooru", library),
                                (workspace / "meloader", "monloader", loader)]:
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/" + name],
                       cwd=repo, env=env, check=True)
    # No inherited activation or protocol handles: reject, never fall
    # through into normal app startup. stdin is explicitly EOF.
    refused = subprocess.run([str(loader), "internal-managed-worker-probe-v1"],
                             stdin=subprocess.DEVNULL, capture_output=True,
                             cwd=directory, env=env, timeout=10)
    assert refused.returncode != 0, refused
    result = subprocess.run([str(library), "internal-managed-probe-v1"],
                            stdin=subprocess.DEVNULL, capture_output=True,
                            cwd=directory, env=env, timeout=90, text=True)
    print(result.stdout, result.stderr, sep="")
    assert result.returncode == 0, result.returncode
    assert "PASS: managed executable probe (no acquisition)" in result.stdout
    crash = subprocess.run([str(library), "internal-managed-crash-probe-v1"],
                           stdin=subprocess.DEVNULL, capture_output=True,
                           cwd=directory, env=env, timeout=100, text=True)
    print(crash.stdout, crash.stderr, sep="")
    assert crash.returncode == 0, crash.returncode
    assert "PASS: managed executable crash recovery (active operation)" in crash.stdout
    assert sorted(p.name for p in directory.iterdir()) == sorted([library.name, loader.name])
