# CLAUDE.md

Context and rules for Claude Code working on this repo. Read fully before generating code.

---

## 1. What this project is

**An attack-resistant, regulator-visible atomic PvP settlement layer for INR corridors, on NPCI's Drunix ledger.**

Two banks in two countries settle the two currency legs of an FX trade so that **neither leg completes unless both do** ("you get paid only if you pay"). INR has no PvP mechanism today (CLS covers only 18 major currencies; INR is not one). On top of the core atomic settlement we add three things that make it substantial rather than a bare swap:

- **Adversarial resistance** — the layer rejects known attacks (double-settle, replay, underfunded, unilateral, poisoned rate), holds a **value-conservation invariant** after every operation, and withstands a **malicious participant org**, not just bad input.
- **Bilateral netting** — settle the *net* of many trades, not each gross, showing the funding-efficiency point that makes PvP economically valuable.
- **Compliance + audit** — signed FX attestation, per-transaction compliance data, and a regulator-visible audit view.

Hackathon: Drunix Hackathon (NPCI × Citi × India Blockchain Forum, CHL-7007).

**The deliverable is a working demo of an attack-resistant INR settlement layer — NOT a production network.**

---

## 2. Non-negotiable rules

1. **Single Drunix network only.** Both banks are orgs on ONE network. NO cross-network / cross-chain atomicity (HTLCs, relays, interop). Future Work.
2. **Atomicity comes from Fabric, not custom code.** One Fabric transaction commits all its writes or none. Model both legs (and, for netting, the whole net set) inside ONE chaincode transaction. Do not hand-roll two-phase commit.
3. **Go for chaincode, not Java.** Java chaincode on Drunix is reported undemonstrated; samples use Go. (VERIFY against github.com/npci/drunix; if Go confirmed, don't revisit.)
4. **Never overclaim** in README/comments/pitch/UI. See §8. We demonstrate a *mechanism* in a *simulated* setting.
5. **Tokenized cash is simulated.** On-ledger balances represent pre-funded currency. We do not create liquidity, touch real money, or move real INR/FX.
6. **Time-boxed.** Assume hard freeze ~30 Sep 2026 unless told otherwise. Prefer a smaller thing that runs over a bigger thing that doesn't. Flag risk to the demo before spending hours.
7. **Build order is protected (§7).** Core settle+rollback must work before any tier-2 feature. Netting is gated behind a Day-1 network go/no-go. Do not build tier-2/3 on an unproven core.

---

## 3. Scope — build this, refuse that

**Orgs (one Drunix network):**
- `BankIN` — holds tokenized INR.
- `BankFX` — holds tokenized foreign currency.
- `Oracle` — publishes the signed FX rate.
- `Auditor` — read-only; regulator's view.

**IN scope — core:**
- `settleTrade()` — atomic gross settlement of one trade, both legs in one tx.
- Endorsement policy `AND(BankIN, BankFX)` — no unilateral settlement.
- Balances/init, query functions, settlement history.

**IN scope — Feature 1: Adversarial resistance (threat model).**
- Chaincode must reject every attack in the §6 threat matrix, each with a distinct error.
- **Value-conservation invariant:** after every settlement/netting op, total tokenized value across all orgs is unchanged (no operation creates or destroys value). Checked in-chaincode and asserted in tests, including under attack.
- **Malicious-org scenario:** model one bank org as actively hostile (forge a settlement, replay, collude with a bad oracle, attempt unilateral move). Show the network holds — resistance to a bad *participant*, not just bad input.
- **FX-oracle-manipulation defense:** settlement accepts a rate only via a signed `Oracle` attestation; unsigned / out-of-band / stale rates are rejected.
- Delivered as a test suite + a live demo of rejections.

**IN scope — Feature 2: Bilateral netting.**
- `netSettle(batch)` — compute the net obligation between BankIN and BankFX across a batch of trades, then settle the single net position atomically in one tx.
- Show gross-vs-net: N trades → 1 (or few) net movements. Metric on screen: gross count/value vs net value settled.
- **GATED — see §7. Only after core + Feature 1 work AND network was solid by end of Day 1.**

**IN scope — Feature 3: Compliance + audit.**
- `Oracle` posts a **signed FX-rate attestation** (named source); settlement references it.
- Per-transaction compliance record (purpose code + simulated AML/sanctions result) in a **private data collection** (visible to relevant banks + Auditor only).
- `Auditor` read-only org + audit view: full reconciled history, FX provenance, compliance status.
- *Private data collections add setup complexity — if time is tight, keep the Auditor org + history (cheap) and drop private-data down to a normal restricted record. Don't drop the audit story entirely.*

**OUT of scope — do not build, even if asked mid-flow without explicit override:**
- Cross-network / interop / HTLCs / two-chain atomic swaps.
- Real wholesale-CBDC / e-Rupee integration (may say "CBDC-ready", nothing more).
- **Multilateral** netting (3+ parties) — bilateral only. Multilateral is Future Work.
- Real FX feeds, real liquidity provisioning, multi-corridor, KYC onboarding, production hardening.
- A bolted-on ML fraud model. The security story here is deterministic rejection, not ML.

If a request lands in OUT scope, say so and propose the in-scope substitute instead of silently building it.

---

## 4. Tech stack

- **Ledger:** Drunix (enhanced Hyperledger Fabric v2.5.x-compatible fork), permissioned, Raft ordering, private data collections available.
- **Chaincode:** Go.
- **Gateway/API:** Go (Fabric Gateway SDK) preferred; Node or Python FastAPI acceptable — pick one, stay consistent.
- **Frontend:** lightweight (React or plain). Legible > fancy. Needs: 2 bank dashboards, settlement screen, auditor view, a gross-vs-net panel.
- **Runtime:** Docker + (Windows) WSL2.

---

## 5. Environment / setup gotchas

- **Drunix is not vendored here.** Clone at repo root: `git clone https://github.com/npci/drunix.git drunix`, git-ignore it. It generates real key/MSP/TLS material locally — never commit it.
- **Verify actual test-network bring-up against the cloned repo** — don't invent Fabric commands from memory. One source references chaincode env image `npcioss/drunix-ccenv` (tag may be `1.0`, not `1.0.0` like peer/orderer) — CONFIRM before use.
- **Windows:** Docker Desktop needs WSL2. Home edition → WSL2 is the only backend.
- Keep `NOTES.md` logging every Drunix/Windows deviation — this platform is new and under-documented.
- **KNOWLEDGE GAP:** exact Drunix quirks vs stock Fabric (SQL state DB, private-data path, scaling) not fully known. When Drunix docs are silent, fall back to Fabric v2.5 behaviour and note the assumption.

---

## 6. The demo + the threat matrix

**Threat matrix — each row is a chaincode rejection test AND a live demo beat:**

| Attack | Expected behaviour |
|---|---|
| Double-settle same `tradeId` | Rejected — idempotency, unique tradeId |
| Replay a prior settlement | Rejected — already-settled state |
| Settle while underfunded | Rejected — full rollback, no half-state |
| Unilateral settle (one endorsement) | Rejected — `AND(BankIN,BankFX)` not satisfied |
| Poisoned / unsigned / stale FX rate | Rejected — signed-attestation check; out-of-band rate refused |
| Negative / overflow amount | Rejected — input validation, integer minor units |
| Malicious org forges/replays/colludes | Rejected — endorsement + attestation + idempotency hold against a hostile participant |
| Value-conservation breach (create/destroy value) | Impossible — invariant asserted after every op; violating tx rejected |

**Demo run (this is the point — protect the ability to show it):**
1. Happy path — settle, both legs commit.
2. Underfunded leg → full rollback, balances unchanged.
3. Peer down (one bank org offline) → settlement can't complete.
4. Threat rejections — walk 2–3 rows live, incl. the **malicious-org** attempt and the **value-conservation invariant** holding before/after.
5. Netting — submit N trades, show gross-vs-net, settle the net atomically. *(If gated out, show the design slide instead.)*
6. Auditor view — regulator sees reconciled history + FX provenance + compliance status.

---

## 7. Build order (protected) + go/no-go

- **P0 — Network up.** Clone Drunix, bring up sample test-network, confirm peers/orderer/channel. *Highest risk; don't proceed until solid.*
- **P1 — Core.** `initLedger`/balances, `settleTrade()` atomic both-leg, query, endorsement `AND(BankIN,BankFX)`.
- **P2 — Feature 1 (threat model).** Implement input validation, idempotency, attestation check; write the rejection test suite.
- **P3 — Feature 3 (compliance + audit).** Oracle attestation, compliance private data, Auditor org + view.
- **P4 — Feature 2 (netting) — GATED.** Build only if: network was solid by **end of Day 1** AND P1–P3 done. Else → netting becomes a Future-Work design slide (show the algorithm, don't ship broken code).
- **P5 — Pitch.** README + deck matching §8; rehearse Q&A.

**Go/no-go on netting: decide end of Day 1.** Half-built netting scores below clean deferral. Do not let netting endanger P1–P3.

**Fallback (decide end of Day 1):** if Drunix network won't come up, run the same Go chaincode on stock Hyperledger Fabric test-network, clearly labelled — last resort only, since "runs on real Drunix" is a scoring point.

---

## 8. Claiming discipline (ALL generated prose)

Separate these, never blur:

- **FACT:** INR has no PvP coverage; CLS settles 18 major currencies only; widening PvP to emerging-market currencies is prioritized by BIS, the G20 cross-border roadmap, and the Basel Committee. (Sources: BIS Triennial Survey 2025 / BIS Quarterly; CLS Group.)
- **OUR IMPLEMENTATION:** atomic two-leg settlement on a single Drunix network, dual-bank endorsement, deterministic rejection of a defined threat set (incl. a hostile participant org), a value-conservation invariant checked every op, bilateral netting for funding efficiency, signed FX attestation + regulator-visible audit — all with simulated tokenized cash.
- **LIMITATION:** does not create liquidity; single network only; bilateral (not multilateral) netting; CBDC leg simulated; no real banks/governance.
- **FUTURE WORK:** cross-network atomic interop, real wholesale-CBDC leg, multilateral netting, live compliance engine.

Banned phrasings: "eliminates settlement risk", "solves cross-border payments", "production-ready", "CBDC-integrated", "at scale", "unhackable", "formally verified", "mathematically proven". The invariant is **runtime-checked, not formally proven** — call it a "value-conservation invariant enforced in chaincode", never a proof. Overclaiming is the single biggest scoring risk.

**Positioning vs Citi Token Services (a Citi mentor WILL raise it):** CTS is USD-centric and on Citi's *proprietary* network; ours is INR PvP on NPCI's *neutral shared* utility, with deterministic attack-resistance and regulator visibility. Name CTS proactively; state the delta; don't claim CTS "can't do PvP" (unverified).

**Kill-question answer (one line, everywhere):** "Two banks that don't trust each other need all-or-nothing settlement of two currency legs; a shared database/registry can record it but cannot atomically enforce it — only a ledger with atomic commit and dual-org endorsement can."

---

## 9. Conventions

- Money as integer minor units (paise/cents). Never floats for balances.
- Chaincode deterministic — no time-of-day, no randomness, no external calls inside chaincode.
- Fail closed: on any ambiguity in a settlement, reject rather than partially apply.
- Every state-changing function validates inputs first (amounts, tradeId uniqueness, attestation signature) — this IS the threat model, not an add-on.
- After every settlement/netting op, assert the value-conservation invariant (sum of all org balances constant); reject the tx if it would break.
- Assume any single org may be malicious: never trust one org's assertion alone — require the endorsement + a signed oracle attestation for anything that moves value.
- Keep chaincode small and obviously atomic; readability of the atomic path beats features.
- Verify claims/commands against the real Drunix repo / Fabric docs before asserting; if unverified, label it.
