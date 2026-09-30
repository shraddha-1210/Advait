#!/usr/bin/env bash
# Build and start the gateway the same way on every machine. Run inside WSL,
# from any directory:   bash scripts/run-gateway.sh
# Runs scripts/doctor.sh first and stops with its fixes if anything is wrong.
# Env: DRUNIX_HOME (default /root/drunix), ORACLE_KEY, ADDR (default :8080),
#      BIN_DIR (where the binaries go, default <repo>/gateway/bin, git-ignored).
set -euo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export GOFLAGS="${GOFLAGS:--buildvcs=false}"   # Windows-owned checkout from WSL (NOTES.md E8)
export ORACLE_KEY="${ORACLE_KEY:-$REPO/network/oracle/oracle.key}"
BIN_DIR="${BIN_DIR:-$REPO/gateway/bin}"

PORT="${ADDR:-:8080}"; PORT="${PORT##*:}"
if curl -s -m 3 "http://localhost:$PORT/api/health" | grep -q '"ok":true'; then
  echo "A healthy gateway is already running on :$PORT. Stop it first (pkill -f gateway/bin/gateway) to restart." >&2
  exit 1
fi
if ! bash "$REPO/scripts/doctor.sh"; then
  echo "Not starting the gateway: fix the FAIL lines above, then re-run." >&2
  exit 1
fi
echo "Building gateway, pvpctl and oracle into $BIN_DIR"
(cd "$REPO/gateway" && go build -o "$BIN_DIR/" ./cmd/...)
cd "$REPO/gateway"
exec "$BIN_DIR/gateway"
