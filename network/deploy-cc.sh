#!/usr/bin/env bash
# Deploy (or upgrade) the pvp chaincode on the running Drunix test network.
# Run inside WSL. Usage: network/deploy-cc.sh [version] [sequence]   (defaults 1.0 and 1)
# To upgrade a deployed chaincode, pass a new version and the next sequence,
# e.g. network/deploy-cc.sh 1.1 2
#
# Endorsement policy: AND(BankIN, BankFX). A transaction is valid only if a
# peer of EACH bank executed it and signed the same result.
set -euo pipefail
VERSION="${1:-1.0}"
SEQUENCE="${2:-1}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRUNIX_HOME="${DRUNIX_HOME:-/root/drunix}"
export PATH="$PATH:$DRUNIX_HOME/drunix-network/bin"
export FABRIC_CFG_PATH="$DRUNIX_HOME/drunix-network/config"
# Packaging runs `go` on this Windows-owned checkout from WSL, where git refuses
# to report VCS status ("dubious ownership") and the build fails (NOTES.md E8).
export GOFLAGS="${GOFLAGS:--buildvcs=false}"

cd "$DRUNIX_HOME/drunix-network/test-network"
./network.sh deployCC \
  -ccn pvp \
  -ccp "$REPO/chaincode/pvp" \
  -ccl go \
  -ccv "$VERSION" \
  -ccs "$SEQUENCE" \
  -ccep "AND('Org1MSP.peer','Org2MSP.peer')"
