# Bundled Meloader first-launch pairing — completed milestone

**Status:** merged November 24, 2026: Mebooru #13 (`c5319ee`) and Meloader #24 (`b5b9ddb`). Local Apple Silicon Mac acceptance completed. Linux/Windows/Intel Mac native CI and Meloader CI/CodeQL passed; final hosted ARM Mac check was queued at merge following runner-allocation failures. The operator explicitly accepted this exception. See [validation record](../validation/mac-acceptance-2026-11-24.md). This candidate changes startup trust, not ordinary plugin approval or API permission semantics.

## Behavior and boundary

- Only a macOS first-party app bundle built with the bundled companion flag can bootstrap its exact executable sibling under `Contents/MacOS`. The launcher must acquire the Mebooru listener; the child must acquire its own listener before any credentials are exchanged. A name, health response, loopback address or existing process does not establish trust.
- Both standard per-user profile directories must be newly created by this launch; the companion config is seeded with exclusive creation. Both configs must still have pristine, explicitly local values. Existing directories, custom/portable configs, external executables, environment-pinned routing, paused connections, previously paired profiles and partial resets do not qualify. An intentionally complete reset of both entire profile directories creates a new profile; deleting only TOMLs does not.
- The parent passes two anonymous inherited pipes (FD 3/4) to the child. The fixed-size versioned handshake exchanges two independent random bearer credentials. No new HTTP endpoint, token-bearing browser frame, command argument, environment secret or remote callback is introduced. Parent deadline is 15 seconds. The existing paired scopes are used: Mebooru read/write, not delete; Meloader's existing paired scopes. The normal token hash, config persistence, peer URL, pause and revoke mechanisms remain authoritative.
- The child durably saves first, then acknowledges; the parent saves and sends commit. The child does not start queue/watch/PTR/HTTP work until commit. On observed failure, exact-transaction rollback is attempted. A hard crash or disk failure can leave a one-sided pair; automatic authorization never retries on later launches, and manual pairing/recovery remains available. This is not an atomic transaction across two files.
- This trust model assumes a trusted installed app bundle and OS user. It does not provide isolation from software already able to replace that user's executables or read/write their application data; ad-hoc signing is not publisher identity. Standalone and unknown remote plugins still require ordinary approval. Mebooru remains WAN-isolated; no blacklist, gallery-dl, rating or ingestion semantics are changed.

## Verification gates

The intended CI gate is both repositories' existing CI; the one-time, explicitly authorized hosted Apple Silicon runner exception for this milestone is documented above and in the validation record. It is not a standing waiver. In Mebooru the paired Mac smoke additionally checks fresh profile credentials and their hashes, correct scopes and CSP, two authenticated API directions, preserved token IDs on restart, real API revocation and refusal afterward, partial config reset refusal, occupied-port impersonation refusal, and child shutdown. Unit tests cover invalid frames, persistence failure/rollback, pristine configuration and exact grant/rollback. A smoke fixture completes setup by setting the disposable config flag while stopped; it is not a claim that the Finder setup wizard was exercised.

Operator acceptance on Apple Silicon Mac covered the following workflow (see the validation record for the exact observed evidence; not every automated negative path was manually repeated):
1. Build both explicitly pinned candidate revisions from the new project root. Keep existing real profiles and database untouched. Confirm ports 8455/8456 are free after quitting the existing app.
2. Launch with an isolated temporary HOME in the QA directory (Terminal invocation of the app executable), finish first-run setup with the disposable gallery and local-only reach. The Loader tab should connect without Connect/Approve. Confirm normal Finder behavior in a separate new macOS user profile if desired; never reset the actual user library just to simulate fresh Finder launch.
3. Restart, pause/resume, and revoke. A revoked or paused link must not regenerate credentials. Confirm existing manually paired real profile keeps its pairing after upgrade. For a bounded acquisition test, retain strict pre-download blacklist verification and verify selected destination and source metadata. Do not copy private tokens, actual media or credentials into GitHub.
4. Review port conflict/crash/partial reset behavior, security checks and all CI before merging the paired PRs. The two repos have independent histories: update the companion pin to the verified merged SHA before a release.

## Operator's Mac test root

Use `$HOME/Downloads/Projects/Coding/Mebooru` for all newly downloaded test source checkouts, builds and disposable QA profiles. Keep each candidate under its own `testing/` directory with both source SHAs recorded. Existing gallery media, databases, configuration, archives and runtime state must remain in their current locations unless explicitly directed otherwise.

Example candidate build after checks, substituting the approved full PR head SHAs:
```sh
root="$HOME/Downloads/Projects/Coding/Mebooru"
mkdir -p "$root/testing"
qa_dir=$(mktemp -d "$root/testing/bundled-pairing.XXXXXX") &&
  git clone --branch bundled-meloader-bootstrap https://github.com/jwebb7847-ui/mebooru.git "$qa_dir/mebooru" &&
  git clone --branch bundled-meloader-bootstrap https://github.com/jwebb7847-ui/meloader.git "$qa_dir/meloader" &&
  git -C "$qa_dir/mebooru" checkout --detach MEBOORU_SHA &&
  git -C "$qa_dir/meloader" checkout --detach MELOADER_SHA &&
  (cd "$qa_dir/mebooru" && sh packaging/build-macos-app.sh "$qa_dir/meloader" "$qa_dir") &&
  printf '%s\n' "$qa_dir/Mebooru.app"
```
Use only actual verified SHAs, not the literal placeholders. Do not launch until ports are clear.
