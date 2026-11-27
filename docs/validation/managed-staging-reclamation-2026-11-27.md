# II.6.4c — proposed native reclamation checkpoint (2026-11-27)

**Status:** proposed, not passed. Do not ask the owner to execute until exact consolidated Linux/Windows development gates pass and the code SHA is fixed. [PR #38](https://github.com/jwebb7847-ui/mebooru/pull/38) remains draft, no merge authorization. This checkpoint covers the initial isolated strict same-volume single-image candidate, not full production import parity, custody/dispatch, arbitrary media, batches or II.6.4d/e acceptance.

## Hosted prerequisites

- Exact combined candidate at time of writing: `303dc3b5f12ac50409f08f221156dc82cd2551d4`, [45998529586](https://github.com/jwebb7847-ui/mebooru/actions/runs/45998529586). Require Linux all-module + verbose isolated staging, all five Windows staging groups (baseline, workspace, prepared, sealed, closure), separate Windows ownership/runtime and actual pinned standalone/Python probes. Verify logs and exact SHA; record any failures, changes or superseded runs.
- Previously verified exact workspace code `6240d5c892911e8c1dbba34e50bebd570388bdb9`, [45996847632](https://github.com/jwebb7847-ui/mebooru/actions/runs/45996847632): both Linux and Windows succeeded; 26 workspace scenarios each, actual pinned alias-aware transfer, standalone 1.32.19 and brokered offline drain. Windows full staging 505.514s, workspace 108.61s. Prior 503 runs were not accepted.
- Temporary QA-only #39 ([45998011497](https://github.com/jwebb7847-ui/mebooru/actions/runs/45998011497)) tests the complete application code independently; it is not a substitute for required #38 checks, and must be closed without merge after documenting results.

## Native owner test plan (only once authorized for an exact verified SHA)

Use fresh unique checkout under existing Mebooru testing directory. Never operate on real profile or gallery. No sudo, ordinary app startup, live source acquisition, real credentials or modification/deletion of existing test folders. Go modules and pinned Python runtime may use network for setup; runtime fixtures use synthetic sources and controlled loopback only. Hosted standalone x86-64 test remains mandatory; no pinned Mac standalone asset. Keep transcript and exact SHA. Current proposed SHA below must be replaced if corrected code is required.

```bash
(
set -euo pipefail
PROJECT="$HOME/Downloads/Projects/Coding/Mebooru"
REV="303dc3b5f12ac50409f08f221156dc82cd2551d4"
mkdir -p "$PROJECT/testing"
TESTDIR="$(mktemp -d "$PROJECT/testing/staging-reclamation.XXXXXX")"
git clone --no-checkout https://github.com/jwebb7847-ui/mebooru.git "$TESTDIR/mebooru"
cd "$TESTDIR/mebooru"
git checkout --detach "$REV"
export GOWORK=off GOTOOLCHAIN=local
mkdir -p "$TESTDIR/tmp"
export TMPDIR="$TESTDIR/tmp"
go version
uname -m
git rev-parse HEAD
go test -race -count=1 -v -timeout=15m ./internal/acquisitionstage ./internal/acquisitionpolicy ./internal/library
go vet ./internal/acquisitionstage ./internal/acquisitionpolicy ./internal/library
(cd shared; go test -race -count=1 -v -timeout=5m ./acquisitionplan ./workerproto ./processguard ./contract; go vet ./acquisitionplan ./workerproto ./processguard ./contract)
(cd meloader; go test -race -count=1 -v -timeout=5m ./internal/pipeline ./internal/taxonomy ./internal/gdl; go vet ./internal/pipeline ./internal/taxonomy ./internal/gdl)
MEBOORU_PROBE_PINNED_RUNTIME=1 MEBOORU_PROBE_STANDALONE_RUNTIME=0 python3 .github/qa/managed-probe.py
echo "PASS: Mac isolated II.6.4c reclamation checkpoint"
)
```

## Evidence required

Confirm Go/native architecture, exact commit, all 101 composed receipt scenarios and 98 reclamation/closure scenarios (26 workspace + 26 prepared + 28 sealed + 18 closure), earlier isolated staging tests, required race/vet commands and actual pinned runtime probes. Record skips accurately (optional live tests deliberately excluded; pinned runtime initial skip followed by real probe PASS required). Record per-package and top-level test timings for future performance review, including current repeated byte/evidence checks. Final marker only after every requested command succeeds. If test fails, preserve fixture/transcript and stop; no force cleanup or weakened checks. This qualifies only the implemented isolated scope. Actual production custody/authority and scheduler remain subsequent gates, broader media and batch parity, history scalability, and full native/race/vet/CodeQL/container acceptance must still be addressed.
