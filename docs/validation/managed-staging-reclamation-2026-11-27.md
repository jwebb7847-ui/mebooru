# II.6.4c — native reclamation checkpoint (2026-11-27)

**Status:** hosted and owner native Mac checkpoint passed. [PR #38](https://github.com/jwebb7847-ui/mebooru/pull/38) remains draft, no merge authorization. This checkpoint covers the initial isolated strict same-volume single-image candidate, not full production import parity, custody/dispatch, arbitrary media, batches or II.6.4d/e acceptance.

## Hosted prerequisites

- Exact combined candidate at time of writing: `303dc3b5f12ac50409f08f221156dc82cd2551d4`, [45998529586](https://github.com/jwebb7847-ui/mebooru/actions/runs/45998529586). Require Linux all-module + verbose isolated staging, all five Windows staging groups (baseline, workspace, prepared, sealed, closure), separate Windows ownership/runtime and actual pinned standalone/Python probes. Verify logs and exact SHA; record any failures, changes or superseded runs.
- Previously verified exact workspace code `6240d5c892911e8c1dbba34e50bebd570388bdb9`, [45996847632](https://github.com/jwebb7847-ui/mebooru/actions/runs/45996847632): both Linux and Windows succeeded; 26 workspace scenarios each, actual pinned alias-aware transfer, standalone 1.32.19 and brokered offline drain. Windows full staging 505.514s, workspace 108.61s. Prior 503 runs were not accepted.
- Temporary QA-only #39 ([45998011497](https://github.com/jwebb7847-ui/mebooru/actions/runs/45998011497)) tests the complete application code independently; it is not a substitute for required #38 checks, and must be closed without merge after documenting results.

## Native owner test procedure (completed at exact verified SHA)

Use fresh unique checkout under existing Mebooru testing directory. Never operate on real profile or gallery. No sudo, ordinary app startup, live source acquisition, real credentials or modification/deletion of existing test folders. Go modules and pinned Python runtime may use network for setup; runtime fixtures use synthetic sources and controlled loopback only. Hosted standalone x86-64 test remains mandatory; no pinned Mac standalone asset. Keep transcript and exact SHA. Use the exact hosted-tested SHA below; do not substitute a moving branch head.

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

## Hosted results



**Hosted reclamation lifecycle verified; owner Mac checkpoint requested.** Exact application and workflow SHA `303dc3b5f12ac50409f08f221156dc82cd2551d4`, [45998529586](https://github.com/jwebb7847-ui/mebooru/actions/runs/45998529586): all seven draft jobs SUCCESS. Linux 140503947303 all 199 named composed/cleanup subtests (37 new, 29 held, 35 restored, 26 workspace, 26 prepared, 28 sealed, 18 closure), staging 91.823s all-module run / 78.189s dedicated run. Windows 140503947379 baseline 19 top-level tests incl all 101 composed cases (318.739s; one inherited root-replacement skip because OS refuses rename of open root, not counted as pass); 140503947550 workspace 26/26 (101.146s); 140503947425 prepared 26/26 (189.128s); 140503947487 sealed 28/28 (200.792s); 140503947620 closure 18/18 (144.104s). Windows staging partitions collectively covered 23 enumerated top-level tests; no failed assertions. Windows ownership/runtime 140503946976 and Linux both confirmed actual alias-aware strict preflight/contained transfer, pinned Python and standalone 1.32.19 through custody, two offline-drain markers each. Full ready-PR native matrix/race/vet/container/CodeQL not run. Auxiliary #39 Linux 140502203876 SUCCESS staging 148.614s; closed without merge after recording result. No production activation, no change to integration branch. Next: owner Mac disposable test at exact tested SHA; this runbook. Narrow isolated scope, not full II.6.4c acceptance or ordinary acquisition parity; 1024-record limit, media/batch/claim combinations and production authority remain explicit.

## Evidence required

Confirm Go/native architecture, exact commit, all 101 composed receipt scenarios and 98 reclamation/closure scenarios (26 workspace + 26 prepared + 28 sealed + 18 closure), earlier isolated staging tests, required race/vet commands and actual pinned runtime probes. Record skips accurately (optional live tests deliberately excluded; pinned runtime initial skip followed by real probe PASS required). Record per-package and top-level test timings for future performance review, including current repeated byte/evidence checks. Final marker only after every requested command succeeds. If test fails, preserve fixture/transcript and stop; no force cleanup or weakened checks. This qualifies only the implemented isolated scope. Actual production custody/authority and scheduler remain subsequent gates, broader media and batch parity, history scalability, and full native/race/vet/CodeQL/container acceptance must still be addressed.


**Native Mac reclamation checkpoint — PASS (2026-11-27).** Operator supplied complete terminal transcript for exact detached `303dc3b5f12ac50409f08f221156dc82cd2551d4`, Go 1.27.4 darwin/arm64. All 199 named composed receipt/reclamation subtests passed, including 37 new, 29 held, 35 restored, 26 workspace, 26 prepared, 28 sealed and 18 closure. 246 top-level PASS entries and 546 subtest PASS entries across initial tests and pinned reruns; no FAIL, race warnings, or timeout; all ten requested race-enabled package runs and corresponding vet commands completed (set -e, final marker). Seven initial skips: five pinned tests subsequently passed (MetadataOperationPinnedRuntime, StrictTransferOperationPinnedRuntime, ManagedStrictTransferPinnedRuntime, StrictBlacklistGalleryDLMediaBoundary, StrictGalleryDLPreflight); two optional live tests intentionally excluded (local Audit_A rating and live taxonomy). Native staging 680.563s (11m20.563s), acquisitionpolicy 16.602s, library 15.849s, acquisitionplan 2.768s, workerproto 13.484s, processguard 28.179s, contract 2.590s, pipeline 1.637s, taxonomy 1.819s, gdl 1.480s. Staging phase timings: new receipt 61.34s, held 34.70s, restored 42.91s, workspace 63.56s, prepared 106.03s, sealed 148.43s, closure 135.65s. Root replacement passed on Mac (Windows inherited skip). Actual executable normal/library-loss/worker-loss, contained and brokered pinned Python gallery-dl 1.32.19, catalog 149777 bytes SHA256 5e47d9936016ba4505e02cba672a00883263ee3113344d82cee45b0edc0cb828, active transfer cancellation and brokered offline drain all passed. Strict alias-aware actual transfer requested only approved unchanged media. Legacy 200-post synthetic fixture both paths 198 approved, one blacklisted, one refused, 197 transferred; per-post 200 taxonomy requests / 3.517135875s total versus batch six incl 429/retry / 2.168521291s. Not a live benchmark. Final marker `PASS: Mac isolated II.6.4c reclamation checkpoint`. Long silent package test was active, not stalled; future runbooks should print phase markers and use separate package invocations or explicitly documented streaming format, preserve timeout and all checks. No full raw transcript, absolute personal paths, or real user data committed.

**Checkpoint disposition:** isolated same-volume single-PNG staged lifecycle and durable receipt/reclamation qualified on Linux, Windows and owner Mac at exact code SHA. This is not full II.6.4c acceptance, merge approval, ordinary media/batch parity, production authorization, or cross-database/power-loss atomicity. Keep existing routes untouched; next perform bounded c exit-gap review against approved design (actual production owner/custody integration boundary, live quota, full supported format and batch/claim parity, bounded ledger scalability and abandonment/recovery policy). Separate production scheduler/dispatch II.6.4d/e and final full native/race/vet/container/CodeQL gates. No additional owner test requested until a meaningful new integrated candidate.


**2026-11-27 isolated qualification closeout / exit review:** the cross-platform single-PNG same-volume receipt and reclamation proof is accepted at exact application SHA `303dc3b5f12ac50409f08f221156dc82cd2551d4`. Full II.6.4c remains active, not ready for unconditional closeout. Original gates still include explicit pre-mutation eligibility, practical media/naming/claim and batch parity, write-time resource limits and destination-aware verified-copy strategy, scalable durable history and safe abandonment, and actual owner/custody/scanner fencing at c/d boundary. See the source-grounded [exit-gap review](../project/managed-staging-exit-review.md). No application changes, merge, production enablement, or new hardware request. Next prioritize exact pre-mutation planning and independent safe extensions, consolidate hosted qualification before next owner gate.
