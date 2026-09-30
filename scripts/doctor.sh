#!/usr/bin/env bash
# Preflight check: is this machine set up to run Advait? Run inside WSL from
# anywhere:  bash scripts/doctor.sh
# Prints OK / WARN / FAIL per check with the fix. Exits 1 if anything FAILs.
# It only reads; it changes nothing.
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRUNIX_HOME="${DRUNIX_HOME:-/root/drunix}"
ORGS="${DRUNIX_ORGS:-$DRUNIX_HOME/drunix-network/test-network/organizations}"
fails=0
ok()   { printf '  [ OK ] %s\n' "$1"; }
warn() { printf '  [WARN] %s\n         fix: %s\n' "$1" "$2"; }
bad()  { printf '  [FAIL] %s\n         fix: %s\n' "$1" "$2"; fails=$((fails+1)); }
ver_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; } # ver_ge have need

echo "Advait doctor  (repo $REPO, DRUNIX_HOME $DRUNIX_HOME)"

echo "Tools"
if [ "$(uname -s)" = Linux ]; then ok "running on Linux/WSL"
else bad "running on $(uname -s), not Linux" "open the WSL (Ubuntu) shell and run this there; the network scripts and gateway need Linux"; fi
if command -v go >/dev/null; then
  gv=$(go env GOVERSION | sed 's/^go//')
  if ver_ge "$gv" 1.25.0; then ok "go $gv (gateway needs >= 1.25.0)"
  else bad "go $gv is too old (gateway needs >= 1.25.0, Drunix build needs 1.26.1)" "install Go 1.26.1 from the official tarball (README step 1)"; fi
else bad "go not found" "install Go 1.26.1 (README step 1) and add /usr/local/go/bin to PATH"; fi
for t in jq curl; do command -v $t >/dev/null && ok "$t" || bad "$t not found" "apt-get install -y $t"; done

echo "Docker"
if ! command -v docker >/dev/null; then
  bad "docker CLI not found in this shell" "Docker Desktop > Settings > Resources > WSL integration: enable your Ubuntu distro (NOTES.md E1)"
elif ! docker info >/dev/null 2>&1; then
  bad "docker daemon not reachable" "start Docker Desktop and wait until it is running"
else
  ok "docker daemon reachable"
  cfg="${DOCKER_CONFIG:-$HOME/.docker}/config.json"
  if [ -f "$cfg" ] && grep -q 'desktop.exe' "$cfg" && ! docker-credential-desktop.exe list >/dev/null 2>&1; then
    warn "docker credential helper desktop.exe is broken in WSL; image pulls and network.sh down will fail (NOTES.md E7)" \
         "mkdir -p /tmp/dockercfg && echo '{}' > /tmp/dockercfg/config.json && export DOCKER_CONFIG=/tmp/dockercfg"
  else ok "docker credentials usable"; fi
  for img in npcioss/drunix-ccenv:1.0 npcioss/drunix-baseos:1.0; do
    docker image inspect "$img" >/dev/null 2>&1 && ok "image $img" || bad "image $img missing (deployCC fails without it, NOTES.md D5)" "docker pull $img"
  done
fi

echo "Drunix"
if [ -d "$DRUNIX_HOME/drunix-network/test-network" ]; then ok "Drunix clone at $DRUNIX_HOME"
else bad "no Drunix clone at $DRUNIX_HOME" "git clone --depth 1 https://github.com/npci/drunix.git $DRUNIX_HOME  (or export DRUNIX_HOME=<your clone>)"; fi
BIN="$DRUNIX_HOME/drunix-network/bin"
if [ -x "$BIN/peer" ] && [ -x "$BIN/configtxgen" ] && [ -x "$BIN/cryptogen" ]; then
  pv=$("$BIN/peer" version 2>/dev/null | awk '/Version:/{print $2; exit}')
  [ "$pv" = 1.0.0 ] && ok "Drunix binaries (peer $pv)" || bad "peer binary is version '$pv', not Drunix 1.0.0 (stock Fabric from network.sh prereq? NOTES.md D1)" "cd $DRUNIX_HOME && make tools orderer && cp build/bin/* drunix-network/bin/"
else bad "Drunix binaries not built in $BIN" "cd $DRUNIX_HOME && make tools orderer && mkdir -p drunix-network/bin && cp build/bin/* drunix-network/bin/"; fi

echo "Network"
if command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  missing=""
  for c in orderer.example.com lp1.org1 lp1.org2 cp.org1 cp.org2 vs1.org1 vs1.org2 yugabyte-org1 yugabyte-org2 hlf_keydb_org1msp hlf_keydb_org2msp; do
    [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" = true ] || missing="$missing $c"
  done
  [ -z "$missing" ] && ok "all 11 network containers running" || bad "not running:$missing" "cd $DRUNIX_HOME/drunix-network/test-network && ./network.sh up && ./network.sh createChannel && bash $REPO/network/add-orgs.sh"
fi
for org in org1 org2 oracle auditor; do
  if [ -d "$ORGS/peerOrganizations/$org.example.com" ]; then ok "crypto material for $org"
  else
    case $org in
      oracle|auditor) bad "no crypto material for $org in $ORGS" "bash $REPO/network/add-orgs.sh" ;;
      *)              bad "no crypto material for $org in $ORGS" "bring the network up (see above), or set DRUNIX_HOME / DRUNIX_ORGS" ;;
    esac
  fi
done

echo "Oracle key"
KEY="${ORACLE_KEY:-$REPO/network/oracle/oracle.key}"; PUB="$REPO/network/oracle/oracle.pub"
if [ ! -f "$KEY" ]; then
  bad "no oracle key at $KEY (it is git-ignored, so every clone makes its own)" \
      "cd $REPO && <bin>/oracle keygen network/oracle, then deploy and init a FRESH ledger: pvpctl init network/oracle/oracle.pub"
elif command -v openssl >/dev/null; then
  derived=$( { printf '302e020100300506032b657004220420' | xxd -r -p; base64 -d < "$KEY"; } 2>/dev/null \
            | openssl pkey -inform DER -pubout -outform DER 2>/dev/null | tail -c 32 | base64)
  filepub=$(tr -d '\r\n ' < "$PUB" 2>/dev/null)
  if [ -z "$derived" ]; then warn "could not read $KEY as an Ed25519 seed" "regenerate: oracle keygen network/oracle (then init a fresh ledger)"
  elif [ "$derived" = "$filepub" ]; then ok "oracle.key matches oracle.pub ($derived)"
  else bad "oracle.key does not match oracle.pub (key $derived, pub $filepub)" "regenerate both: oracle keygen network/oracle, then init a fresh ledger with the new oracle.pub"; fi
else ok "oracle key present at $KEY (openssl missing, match not checked)"; fi

echo "Build and repo"
owner=$(stat -c %u "$REPO" 2>/dev/null)
if [ "$owner" != "$(id -u)" ] && [[ "${GOFLAGS:-}" != *-buildvcs=false* ]]; then
  warn "repo is owned by another user; plain 'go build' fails with 'error obtaining VCS status' (NOTES.md E8)" \
       "use scripts/run-gateway.sh and network/deploy-cc.sh (they set it), or export GOFLAGS=-buildvcs=false"
else ok "go builds will not trip over VCS ownership"; fi
if grep -q $'\r' "$REPO/chaincode/pvp/contract/contract.go" 2>/dev/null; then
  warn "Go files have CRLF line endings (old checkout); the mutation check cannot patch them" \
       "commit or stash your changes first, then: git rm -r --cached -q . && git reset --hard   (re-checks out every file with the .gitattributes line endings)"
else ok "source files have LF line endings"; fi

echo "Gateway port"
ADDR_PORT="${ADDR:-:8080}"; PORT="${ADDR_PORT##*:}"
if curl -s -m 3 "http://localhost:$PORT/api/health" | grep -q '"ok":true'; then ok "a healthy gateway is already running on :$PORT"
elif (exec 3<>/dev/tcp/127.0.0.1/$PORT) 2>/dev/null; then bad "port $PORT is taken by something that is not a healthy gateway" "stop it (ss -ltnp | grep :$PORT) or run with ADDR=:8081 and set VITE_GATEWAY_URL for the frontend"
else ok "port $PORT is free"; fi

echo
if [ "$fails" -eq 0 ]; then echo "doctor: all required checks passed"; exit 0; fi
echo "doctor: $fails check(s) FAILED; fix them in order, top to bottom"; exit 1
