# NOTES.md: Drunix / Windows deviations log

Every place where Drunix or our Windows setup behaved differently from stock Hyperledger Fabric
or from what the docs imply. Newest findings at the bottom of each section.

Drunix commit tested: `ddc0eae` (github.com/npci/drunix, HEAD on 2026-09-26).

---

## Environment (Windows 11 Home + WSL2)

| # | Finding | What we did |
|---|---|---|
| E1 | Docker Desktop's default WSL distro was `docker-desktop`, so `docker` was not available inside `Ubuntu`. | Set `IntegratedWslDistros: ["Ubuntu"]` in `%APPDATA%\Docker\settings-store.json` (backup: `settings-store.json.bak-advait`) and `wsl --set-default Ubuntu`. Restarted Docker Desktop. |
| E2 | WSL2 defaulted to ~7.7 GB RAM. The Drunix test network runs 11 containers, including 2x YugabyteDB. | Added `%USERPROFILE%\.wslconfig` with `memory=10GB, processors=10, swap=4GB`. Needs `wsl --shutdown` to apply. |
| E3 | Building Go and running Fabric from `/mnt/c` (9p filesystem) is slow and can break. | The Drunix working copy lives in WSL at `/root/drunix`. Our own code stays in `C:\Advait` and is read from `/mnt/c/Advait` when packaging chaincode. The `C:\Advait\drunix` clone is for reading only and is git-ignored. |
| E4 | Ubuntu 26.04 in WSL had no `go`, `jq`, or `make`. | `apt-get install golang-go jq make build-essential`. That gives Go 1.26.1, which matches `go.mod` in Drunix. |
| E5 | Git Bash rewrites `/root/...` paths passed to `wsl.exe`. | Prefix with `MSYS_NO_PATHCONV=1`. |

## Drunix vs stock Fabric

| # | Finding | Impact / what we did |
|---|---|---|
| D1 | `./network.sh prereq` downloads **stock Hyperledger Fabric** binaries and images (`install-fabric.sh` from hyperledger/fabric), not Drunix ones. The compose files use `npcioss/drunix-*:1.0.0`. | We did **not** use `prereq`. We built the matching binaries from source: `make tools orderer` in `/root/drunix` (51 s), then copied `build/bin/*` to `drunix-network/bin/`. `peer version` reports `1.0.0`, which matches the images. |
| D2 | The test network has only **2 peer orgs** (Org1MSP, Org2MSP). It has no `addOrg3` script, and `configtx.yaml` defines only Org1 and Org2. | See **Open decision A** below. Our design calls for 4 orgs (BankIN, BankFX, Oracle, Auditor). |
| D3 | Each org runs a **lite peer** (`lp1`, ports 7051/9051), a **committing peer** (`cp`, 7061/9061) and a **validation server** (`vs1`, 7071/9071). Chaincode is installed on and executed by the **lite peers only**. Committing peers return `chaincode ... is not installed`. | Endorse and query against the lite peers `localhost:7051` (Org1) and `localhost:9051` (Org2). |
| D4 | `peer chaincode invoke --waitForEvent` against the lite peers times out (`timed out waiting for txid on all peers`), even though the transaction commits. The lite peers do not seem to serve commit events. | **KNOWLEDGE GAP:** how the Fabric Gateway SDK's commit-status wait behaves on Drunix. We will resolve this when building the gateway. |
| D5 | The chaincode builder image `npcioss/drunix-ccenv:1.0` (two-digit tag, from `core.yaml` `$(TWO_DIGIT_VERSION)`) is **not pulled** by `network.sh up`. The first `deployCC` failed with `No such image: npcioss/drunix-ccenv:1.0`. | `docker pull npcioss/drunix-ccenv:1.0 npcioss/drunix-baseos:1.0`. Both tags `1.0` and `1.0.0` exist on Docker Hub. This confirms the CLAUDE.md §5 warning. |
| D6 | Default state DB is **YugabyteDB** (SQL), plus KeyDB per org. | Worked out of the box with `-s yugabyte` (the default). |
| D7 | `./network.sh cc invoke` sends the proposal to **one org only**. Under the default MAJORITY endorsement policy, the transaction is endorsed (status 200) but its writes **never reach state** (the sample `InitLedger` produced no assets). | This is real evidence that a unilateral endorsement is rejected at commit on Drunix. For proper invokes, target both lite peers (see `scripts/`). |
| D8 | Block metadata `TRANSACTIONS_FILTER` fetched from the committing peer showed `0` (VALID) for the single-org transaction, whose writes were not applied. | **KNOWLEDGE GAP:** Drunix seems to record validation results elsewhere (the "sparse block" / org-chain machinery). We prove rejections by **state** and by the gateway's commit status, not by raw block flags. |
| D9 | The orderer runs with `ORDERER_GENERAL_MVCCENABLED=false`. In Drunix this flag enables **orderer-side** MVCC pre-validation (`common/sparseblocks/sparseblockgenerator.go`). The peer validator also has a `doMVCCValidation` flag (`core/ledger/kvledger/txmgmt/validation/validator.go`). | **KNOWLEDGE GAP (security-relevant):** whether concurrent conflicting settlements can both commit. To be settled **empirically** with a concurrent double-spend integration test. If it fails, set `ORDERER_GENERAL_MVCCENABLED=true` and retest. |

## Open decisions

**A. Four orgs vs two.** The Drunix test network gives us two endorsing orgs. Options:
1. Two Fabric orgs are the two banks (Org1MSP = BankIN, Org2MSP = BankFX). The Oracle is a pinned signing key whose attestations the chaincode verifies. The Auditor is a separate client identity with a read-only role enforced by the chaincode. No network changes.
2. Add Oracle and Auditor as **peerless MSPs** in the channel config: cryptogen + configtx + a channel update, with no extra containers. They become real channel members with their own MSP IDs.
3. Full 4-org network with peers. This needs roughly 10 more containers, including YugabyteDB, and will not fit in 10 GB.

The chaincode reads its role-to-MSP mapping from config set at init, so it works unchanged under 1 or 2.
