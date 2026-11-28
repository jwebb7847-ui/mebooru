# Mebooru — engineering instructions for Codex and future agents

Updated 2026-11-28. Read this first. The owner has directed a change in development priorities: **finish the current worker integration without dismantling the work already done, then prioritize a finished, usable application.** This file supersedes older instructions about sequencing, exhaustive pre-integration gates and scope expansion; the product requirements and technical evidence in the linked documents remain authoritative. Do not interpret old exhaustive acceptance lists as permission to expand this milestone indefinitely.

## Mission and engineering judgment

Mebooru is a personal local-first media library evolved from Monbooru/Monloader. Deliver one install, one UI, one lifecycle, independently managed acquisition worker, reliable manual and opt-in recurring acquisition, and convenient self-contained runtime. The existing download process was functional before this migration. Its replacement is not a new product objective. Do not restart, rewrite, or remove the substantial existing acquisition architecture merely to simplify it. Complete the practical integration of what is already built.

**Moratorium:** no new defensive subsystem, ledger, generalized framework, security architecture, exhaustive fault matrix, or speculative refactor unless a concrete defect blocks normal user workflow or risks actual media loss, blacklist/rating integrity, or explicit offline behavior. For every proposed increment ask: what does the user gain, what happens if deferred, and is the benefit worth the time/complexity? Defer nonblocking rare cases and final hardening to a single concise backlog; do not silently discard product requirements or claim deferred tests passed. Reuse existing library and worker services. Prefer the smallest coherent change that enables a real workflow. Stop development of II.6 once its practical exit criteria are satisfied; do not use "finish the milestone" to invent more prerequisites.

## Read in this order

1. `docs/project/cold-start.md` — exact resume checkpoint and code navigation (last sections newest; some earlier entries superseded).
2. `MEBOORU.md` — product north star, approved decisions and chapter roadmap; use this file for current priority overrides.
3. `docs/validation/managed-guarded-acquisition-2026-11-28.md` — actual integrated evidence. Read other documents and source as needed, not every historical log.
4. Live PR #38, refs, comments and CI: https://github.com/jwebb7847-ui/mebooru/pull/38. Re-fetch before editing; distinguish tested SHA from docs-only descendants.

## Transition checkpoint

- Repo `jwebb7847-ui/mebooru`; integration base `mebooru-foundation` at `c03761ea0aa2feb4da9c67ae509d65e7193671e2` at handoff. Do not develop on upstream-derived main or the frozen standalone Meloader repo.
- Draft PR #38 head `worker/operation-migration` at `360113279069d0804a6d44076eef1e609ded0380`. Do not merge or change readiness without owner approval.
- This transition branch descends from `worker/ii64-durable-submission-prep` (four commits ahead of PR head, exact prepared code `1315b4b4969692e37ac3db71d9deee1bee05aa24`). Includes the previously uncompiled submission authority work. Preserve both original branches. Verify ancestry before proposing a fast-forward or PR update.
- Latest previously fully green development baseline `a0e054f8836aa089a4347e85ac51d4f5677e54c2`, run 46121534358: seven development jobs passed; real sibling binaries, pinned gallery-dl, synthetic loopback source, SQLite; Linux 200 listed/199 imported/1 blocked and 3/2/1; Windows 3/2/1. Not normal application startup or live site proof.
- Corrected primary run 46124547077: six of seven development jobs succeeded (Linux fast and Windows worker included); Windows baseline staging job timed out after 15 minutes (job 140916136683). Full ready-PR jobs skipped. Inspect the actual running test/stack and prior successful baseline before deciding whether to split/re-run or fix a hang. Do not treat timeout as proof of a code defect or of success.
- II.6.4a owner-accepted; b isolated verified; c real disposable bulk path demonstrated; d/e production integration incomplete. Normal application startup still transitional. Prepared submission code not yet qualified.

## First assignment: finish II.6, then stop expanding it

Work autonomously through a coherent implementation block rather than asking for permission at every step.

1. Establish a functioning build/test environment: all three Go modules (root, meloader, shared), Go version from go.mod (currently 1.27.4), GOWORK=off, Python and pinned gallery-dl 1.32.19 for strict runtime tests. Read .github/workflows/native.yml and .github/qa/managed-probe.py for existing commands. Use local task-shell compile/test/fix loops; reserve CI for cross-platform or consolidated verification. Do not downgrade Go or relax strict runtime checks just to get green results. Report environment blockers.
2. Compile and qualify prepared submission changes; fix concrete failures. Inspect the latest Windows timeout; avoid repeatedly running the entire matrix for minor edits.
3. Wire the existing managed worker into normal application startup, ordinary UI-driven explicit acquisitions, configured gallery/runtime/policy, and library publication. Reuse existing adapters and the durable authority machinery already implemented, rather than extending its design. Provide the minimum lifecycle, queue/scheduler and relevant operation dispatch needed for ordinary use, offline control, shutdown and restart. Preserve legacy behavior where practical; do not introduce hidden fallback that violates agreed product boundaries. Surface unsupported ordinary options explicitly and record genuine blockers. Full UI/settings polish and advanced watch features belong to later chapters.
4. Produce a normal executable/app candidate for a disposable profile. Demonstrate an ordinary UI/API user path through real worker/runtime into the chosen gallery, not merely the diagnostic controller. Verify representative single and bulk acquisitions, correct metadata/blacklist/rating, duplicate behavior, cancellation or restart, and offline local browsing. Record basic startup and acquisition wall-clock timings and compare like-for-like where possible. Synthetic local fixtures are fine in CI; actual Mac and live-source tests require explicit owner coordination and isolated profiles.
5. Deliver an exact candidate SHA, build/run instructions, concise test results and deferred items. No claim of final acceptance until owner review. Keep essential regressions fixed; move exotic crash/replay, cross-volume, archival/ledger scale, unusual format combinations, exhaustive adversarial checks and nonblocking compatibility gaps to later refinement. If a deferred item would change a material product requirement, explain and seek owner approval rather than silently dropping it.

## Product requirements and boundaries

- Separate library ownership of gallery DB/media from worker ownership of queue, source and acquisition state. One local product, not a monolith or persistent background service. Explicit offline denies WAN work; local browsing/editing remains available; no unattended acquisition by default. Watches opt-in while app open. No raw arbitrary execution or bypass of source restrictions.
- Preserve original media, multiple galleries, metadata/source provenance, aliases/categories, relationships, duplicate handling, video/animated previews and manual edits. Respect authoritative source ratings; unrated separate from invalid, hidden by default. Strict editable metadata-first blacklist must fail closed and verified aliases must match canonical tags. User wants violent/ill-willed material excluded; adult nonviolent material is not categorically banned. Retain `lore -> meta`, `contributor -> artist`. Do not redesign filtering or add AI filtering now.
- Keep compatibility-critical legacy module paths, config keys, protocol names and on-disk state. Do not touch real profiles or media without explicit authorization and consistent backups. No credential leakage, broad repo permissions, destructive operations or automatic merge.
- User tests on Apple Silicon Mac; use unique disposable folders under `$HOME/Downloads/Projects/Coding/Mebooru/testing`. Existing ffmpeg-full installed; usual historical ports 8455/8456, do not assume unused. No real profile changes. Hosted Linux/Windows checks useful; Mac validation is a meaningful owner checkpoint, not a routine request.

## After II.6 acceptance

Prioritize Chapter III unified Loader/settings/lifecycle and Quit; IV bundled gallery-dl install/update/rollback; V user-facing advanced acquisition/watch functions; VI usability, performance, deferred reliability/security/compatibility and packaging. Deliver vertical, usable workflows first. Do not undertake unrelated feature work or final exhaustive audits before basic product delivery.

## Collaboration / reporting

The owner delegates routine code and testing judgment. Ask only for a genuine material product/trust/privacy decision or an unavoidable hardware test. Work independently in a task branch. Use Git for checkpoints; update concise cold-start pointer with exact SHA, actual evidence, what remains, and next action. Avoid sprawling repeated documentation refreshes and repeated long log dumps. Do not claim success on skipped, timed-out, or unrun tests. Report decisions made, blockers, deferred work, and relevant PR links. No direct edits to integration/main; no force pushes or merges without explicit approval.

The goal is a finished application, not the maximum possible engineering assurance.
