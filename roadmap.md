# ROADMAP.md

Build plan for the Drunix INR PvP settlement layer. Team of 2:
- **Srishti** — owns the entire blockchain stack (network, all chaincode, gateway). Critical path.
- **Shraddha** — owns everything off-ledger: frontend, threat-test design, demo, pitch, docs.

> **Deadline assumption:** hard freeze ~30 Sep 2026. **CONFIRM the real freeze date** — the whole plan compresses around it. Below assumes ~4 working days from 26 Sep.

---

## Full feature set (what "done" includes)

**Core**
- `settleTrade()` — atomic gross settlement, both legs in one tx.
- Endorsement `AND(BankIN, BankFX)` — no unilateral settlement.
- Balances/init, queries, settlement history.

**Feature 1 — Adversarial resistance (threat model)**
- Reject: double-settle (idempotency), replay, underfunded, unilateral, negative/overflow.
- **FX-oracle-manipulation defense** — rate accepted only via signed `Oracle` attestation; unsigned/out-of-band/stale rejected.
- **Value-conservation invariant** — total tokenized value across orgs constant after every op; violating tx rejected. (Runtime-checked, NOT "formally proven".)
- **Malicious-org scenario** — one bank org modeled as hostile (forge/replay/collude); network holds.

**Feature 2 — Bilateral netting — GATED (see go/no-go)**
- `netSettle(batch)` — net obligation between the two banks across a batch, settled atomically in one tx.
- Gross-vs-net metric on screen (N trades → 1 net movement).

**Feature 3 — Compliance + audit**
- `Oracle` signed FX attestation (named source).
- Per-tx compliance record (purpose code + simulated AML) in a **private data collection**.
- `Auditor` read-only org + audit view: reconciled history, FX provenance, compliance status.

**Orgs:** `BankIN`, `BankFX`, `Oracle`, `Auditor`.

---

## Task split

| Area | Owner |
|---|---|
| Chaincode interface contract (Day 0) | **Both — sign off, then freeze** |
| Drunix network bring-up (P0) | **Srishti** |
| All chaincode: settleTrade, validation, idempotency, invariant, attestation-verify, netSettle | **Srishti** |
| Endorsement policy + private data collections | **Srishti** |
| Gateway / API | **Srishti** |
| Frontend: 2 dashboards, settlement, auditor view, gross-vs-net panel | **Shraddha** |
| Threat-test matrix (design the cases) + malicious-org demo framing | **Shraddha designs, Srishti implements in chaincode** |
| Demo script + rehearsal | **Shraddha** |
| README + claiming discipline (CLAUDE.md §8) | **Shraddha** |
| Pitch deck + judge Q&A (kill-question, CTS delta, single-network drawback) | **Shraddha** |
| Architecture diagram | **Shraddha**, review by Srishti |
| Integration + end-to-end debug | **Both** |

Rule: **Shraddha never touches the critical path and never adds load to Srishti** — she works ahead against the interface stub so the ledger being late never blocks UI/pitch.

---

## Phase plan

### Day 0 (26 Sep) — unblock
- **Both:** lock scope, agree + freeze chaincode interface, repo setup, clone Drunix.
- **Srishti:** start P0 — Drunix sample network up. *Don't proceed until peers/orderer/channel solid.*
- **Shraddha:** frontend skeleton + gateway stub (mock balances/responses); draft threat-test matrix.

### Day 1 (27 Sep) — core + threat
- **Srishti:** network solid → `settleTrade()` atomic + endorsement, then Feature 1 (validation, idempotency, invariant, attestation-verify).
- **Shraddha:** dashboards + settlement screen on mock data; finalize threat-case list; draft README claim structure.
- **CHECKPOINT (EOD Day 1):** Is the network reliably up AND core+threat on track? This drives the netting go/no-go and the fallback call. Decide tonight, not Day 3.

### Day 2 (28 Sep) — integrate + compliance/audit
- **Srishti:** Feature 3 (Oracle attestation, compliance private data, Auditor read org); wire gateway to real chaincode.
- **Shraddha:** wire frontend to real ledger; build auditor view; happy-path settlement working end to end.

### Day 3 (29 Sep) — netting (if gated in) + demo + pitch
- **Srishti:** Feature 2 `netSettle` IF go. Else freeze chaincode + write NOTES.md for Q&A.
- **Both:** failure path (underfunded → rollback), peer-down, walk threat rejections incl. malicious-org + invariant.
- **Shraddha:** polish UI, 3-min demo script, pitch deck, rehearse judge Q&A.

### Day 4 (30 Sep) — buffer + submit
- Buffer for slippage (there will be some). Final rehearsal, submission, freeze. **No new features.**

---

## Go/no-go — netting (decide EOD Day 1)
- **GO** only if: network was solid by end of Day 1 AND core + Feature 1 are on track.
- **NO-GO** → netting becomes a Future-Work **design slide** (show the algorithm). Half-built netting scores below clean deferral. Netting must never endanger core/threat/audit.

## Fallback — network (decide EOD Day 1)
- If Drunix won't come up: run the same Go chaincode on stock Hyperledger Fabric test-network, clearly labelled. Last resort only — "runs on real Drunix" is a scoring point.

---

## Risks (ranked)
1. **Single point of failure — all blockchain on Srishti.** New platform, one person, critical path + all new security chaincode + netting. *Mitigation:* Shraddha owns 100% off-ledger so Srishti is never pulled onto UI/pitch; strict scope; honor the Day-1 gate. If Srishti stalls at P0, that's the whole project — watch it closely.
2. **"Knows blockchain" ≠ knows Fabric.** ASSUMPTION to verify NOW: Srishti's background is **Hyperledger Fabric**, not just Ethereum/Solidity. EVM knowledge doesn't transfer (Go chaincode, endorsement policies, no gas, different data model). If EVM-only, P0/P1 risk jumps and Day 0 needs a Fabric ramp block. **Ask her before trusting this timeline.**
3. **Feature stack vs time.** Core + 3 features + netting is a lot for 2 people/4 days. The gate + "off-ledger fully parallel" are what keep it feasible. Invariant and malicious-org are cheap (sum-check + test scaffolding); netting is the expensive one — hence gated.
4. **Overclaiming in pitch.** Handled by Shraddha owning README/deck per CLAUDE.md §8; treat Q&A prep as a deliverable.

---

## Definition of done
- Real Drunix network, orgs BankIN/BankFX/Oracle/Auditor.
- `settleTrade()` atomic; single-endorsement attempt fails; underfunded rolls back; peer-down blocks settlement.
- Threat matrix passes incl. oracle-manipulation, value-conservation invariant, malicious-org.
- Compliance private-data + Auditor view working.
- Netting shipped OR cleanly deferred with a design slide.
- README + deck separating FACT / IMPLEMENTATION / LIMITATION / FUTURE WORK, no banned overclaims.
- Both can answer the kill-question, the CTS delta, and the single-network drawback cold.
