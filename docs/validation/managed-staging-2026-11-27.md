# II.6.4c — isolated staging and new-only receipt checkpoint (2026-11-27)

**Scope:** isolated development candidate, not production activation, complete II.6.4c, or merge approval. [North star](../../MEBOORU.md), [technical design](../project/managed-staging.md), [draft PR #38](https://github.com/jwebb7847-ui/mebooru/pull/38).

Exact test code: `d8c661b7a15ab2f137ed47c0542d95f275227530`. Integration branch unchanged at `c03761ea0aa2feb4da9c67ae509d65e7193671e2`.

## Hosted evidence

- [45970174964](https://github.com/jwebb7847-ui/mebooru/actions/runs/45970174964): extended source details, 15 actual metadata-service modes, readiness cancellation and pinned strict transfer composition, Linux and Windows passed.
- [45969757951](https://github.com/jwebb7847-ui/mebooru/actions/runs/45969757951): corrected inherited pipe shutdown after Windows blocked ReadFile/Close timeout on 45968708640; 12 recovery modes and pinned strict runtime passed both.
- [45970893343](https://github.com/jwebb7847-ui/mebooru/actions/runs/45970893343): first isolated actual new-only PNG import receipt, all 11 modes passed Linux/Windows; pinned strict runtime passed both.
- [45971327188](https://github.com/jwebb7847-ui/mebooru/actions/runs/45971327188): stricter receipt rejected valid local-derived metadata; existing ingest derives library-owned tags independently of the remote source. Do not disable this legacy behavior.
- [45971667545](https://github.com/jwebb7847-ui/mebooru/actions/runs/45971667545): corrected exact baseline of inherited local-derived tags before source writes, independently verified active source attribution and direct-ID union. Linux 140413322359 / Windows 140413322755 passed all 15 new-only receipt subtests and actual pinned strict transfer composition. Full native matrix, all-module race/vet/strict transfer, Docker and CodeQL skipped on draft development gate. Earlier Windows open-root rename subtest skip because OS refused rename is retained, not counted as PASS.

The new-only diagnostic uses actual LandInPlace and existing source/tag/provenance services. Exact receipt binds allocation, policy, sealed manifest/media, actual placed inode, library DB and mapped source details through linked owner records. Existing SHA (including missing images), unsupported parent/collection and missing provenance refused. Unrated fixture remains unrated. Complete approved window, new PNG-only scope, explicit details and isolated gallery assumed. Reopen validates the receipt and actual state without retrying writes. No staging reclamation or production caller; no claim of power-loss atomicity, source authenticity, full duplicate semantics or same-user sandbox. Temporary storage reservation deliberately accounts for three payload copies.

## Owner Mac native checkpoint — passed

Run only against a fresh checkout and disposable fixture data. No real application/profile/gallery mutation or live image-source request; Go module and pinned PyPI installation may need network. Use Go 1.27.4 darwin/arm64 as before. This verifies native local-volume semantics and new inherited pipe cancellation code, not the complete application. Keep output and stop on errors. No sudo, no disabling protections, no deletion of existing test folders.

```bash
(
set -euo pipefail
PROJECT="$HOME/Downloads/Projects/Coding/Mebooru"
REV="d8c661b7a15ab2f137ed47c0542d95f275227530"
mkdir -p "$PROJECT/testing"
TESTDIR="$(mktemp -d "$PROJECT/testing/staging-receipt.XXXXXX")"
git clone --no-checkout https://github.com/jwebb7847-ui/mebooru.git "$TESTDIR/mebooru"
cd "$TESTDIR/mebooru"
git checkout --detach "$REV"
export GOWORK=off GOTOOLCHAIN=local
mkdir -p "$TESTDIR/tmp"
export TMPDIR="$TESTDIR/tmp"
go version
uname -m
git rev-parse HEAD
go test -race -count=1 -v -timeout=5m ./internal/acquisitionstage ./internal/acquisitionpolicy
go vet ./internal/acquisitionstage ./internal/acquisitionpolicy
(cd shared; go test -race -count=1 -v -timeout=5m ./acquisitionplan ./workerproto ./processguard; go vet ./acquisitionplan ./workerproto ./processguard)
(cd meloader; go test -race -count=1 -v -timeout=5m ./internal/pipeline ./internal/taxonomy; go vet ./internal/pipeline ./internal/taxonomy)
MEBOORU_PROBE_PINNED_RUNTIME=1 MEBOORU_PROBE_STANDALONE_RUNTIME=0 python3 .github/qa/managed-probe.py
echo "PASS: Mac isolated II.6.4c receipt checkpoint"
)
```

Owner result: **PASS** (operator-supplied terminal transcript, 2026-11-27). Exact detached SHA matched. Go 1.27.4 darwin/arm64; 221 top-level PASS entries including pinned reruns, 242 subtest PASS entries, zero FAIL or race warnings. All seven requested race-enabled package runs and corresponding vet commands completed (set -e and final marker). Staging alone: 11 top-level tests and 92 subtests. All 15 new-only receipt and 15 mapped-metadata modes passed; custody held-writer/readiness cancellation passed. Package times: acquisitionstage 23.463s, acquisitionpolicy 15.639s, acquisitionplan 1.427s, workerproto 12.123s, processguard 25.820s, pipeline 1.620s, taxonomy 1.814s. Four initial skips (MetadataOperationPinnedRuntime, StrictTransferOperationPinnedRuntime, ManagedStrictTransferPinnedRuntime, StrictBlacklistGalleryDLMediaBoundary) passed in the subsequent pinned Python runtime probe; optional local Audit_A and live taxonomy cache tests intentionally omitted. Actual executable crash recovery, worker loss, version 1.32.19, catalog stream 149777 bytes with expected SHA256, active cancellation, offline drain and strict alias-aware media fixture all passed. Legacy 200-post synthetic preflight/transfer: 198 approved, one blacklisted, one refused, 197 transferred in both runs; per-post 200 requests / 3.383838667s total vs batch six requests including 429/retry / 2.151291584s total. Not a live-site performance measurement. Final marker: `PASS: Mac isolated II.6.4c receipt checkpoint`. No personal absolute paths or full terminal log committed. Operator supplied successful test output; checkpoint verified, not explicit approval of complete milestone or merge. Next: complete duplicate-drop/restore and full relation/naming/job-extra behavior, receipt/reclamation and production scanner/custody authority; II.6.4d/e integrated testing remains mandatory. Do not infer complete milestone acceptance from this checkpoint.
