#!/usr/bin/env bash
# Add OracleMSP and AuditorMSP to the running Drunix test network's channel
# as peerless member orgs (their own CA, admin and client identity; no peers).
# Run inside WSL after `network.sh up` and `network.sh createChannel`, and
# before deploying the chaincode. Safe to re-run: it does nothing if both orgs
# are already on the channel.
#
# The same channel config update also pins three Application policies that
# are ImplicitMeta MAJORITY by default. With 2 orgs MAJORITY means "both
# banks"; with 4 orgs it would mean "any 3", which the two peerless orgs
# could help satisfy (Admins) or which could never be met at all because
# they have no peers (LifecycleEndorsement, Endorsement). Each is set to the
# explicit rule that MAJORITY meant before, so nothing is weakened:
#   Admins               AND('Org1MSP.admin','Org2MSP.admin')
#   LifecycleEndorsement AND('Org1MSP.peer','Org2MSP.peer')
#   Endorsement          AND('Org1MSP.peer','Org2MSP.peer')
# The pvp chaincode's own policy AND('Org1MSP.peer','Org2MSP.peer') is set
# at deploy time (network/deploy-cc.sh) and is not touched here.
set -eo pipefail   # no -u: the Drunix helper scripts read unset variables
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRUNIX_HOME="${DRUNIX_HOME:-/root/drunix}"
CHANNEL="${CHANNEL:-mychannel}"
export PATH="$PATH:$DRUNIX_HOME/drunix-network/bin"
TESTNET="$DRUNIX_HOME/drunix-network/test-network"
cd "$TESTNET"
export TEST_NETWORK_HOME="$TESTNET"
. scripts/configUpdate.sh >/dev/null   # fetchChannelConfig, createConfigUpdate, setGlobals
export FABRIC_CFG_PATH="$DRUNIX_HOME/drunix-network/config"
ART="$TESTNET/channel-artifacts"
WORK="$TESTNET/advait-orgs"             # scratch dir for generated org definitions
mkdir -p "$WORK"

fetchChannelConfig 1 0 "$CHANNEL" "$ART/advait_config.json" >/dev/null 2>&1
if jq -e '.channel_group.groups.Application.groups | has("OracleMSP") and has("AuditorMSP")' "$ART/advait_config.json" >/dev/null; then
  echo "OracleMSP and AuditorMSP are already on channel $CHANNEL; nothing to do."
  exit 0
fi

# 1. Crypto material (cryptogen, like the rest of the test network).
for org in oracle auditor; do
  if [ ! -d "organizations/peerOrganizations/$org.example.com" ]; then
    cryptogen generate --config="$REPO/network/orgs/crypto-config-$org.yaml" --output=organizations
  fi
done

# 2. Org definitions as channel-config JSON. configtx.yaml resolves MSPDir
#    relative to its own directory, so it is copied next to organizations/.
cp "$REPO/network/orgs/configtx.yaml" "$WORK/configtx.yaml"
FABRIC_CFG_PATH="$WORK" configtxgen -printOrg OracleMSP  > "$WORK/OracleMSP.json"
FABRIC_CFG_PATH="$WORK" configtxgen -printOrg AuditorMSP > "$WORK/AuditorMSP.json"

# 3. Modified config: add both orgs, pin the three Application policies.
sig() { # role -> AND(Org1MSP.<role>, Org2MSP.<role>) as a config policy
  jq -n --arg role "$1" '{type: 1, value: {version: 0,
    identities: [
      {principal_classification: "ROLE", principal: {msp_identifier: "Org1MSP", role: $role}},
      {principal_classification: "ROLE", principal: {msp_identifier: "Org2MSP", role: $role}}],
    rule: {n_out_of: {n: 2, rules: [{signed_by: 0}, {signed_by: 1}]}}}}'
}
jq --slurpfile oracle "$WORK/OracleMSP.json" --slurpfile auditor "$WORK/AuditorMSP.json" \
   --argjson peers "$(sig PEER)" --argjson admins "$(sig ADMIN)" '
  .channel_group.groups.Application.groups.OracleMSP  = $oracle[0]
| .channel_group.groups.Application.groups.AuditorMSP = $auditor[0]
| .channel_group.groups.Application.policies.Admins.policy               = $admins
| .channel_group.groups.Application.policies.LifecycleEndorsement.policy = $peers
| .channel_group.groups.Application.policies.Endorsement.policy          = $peers
' "$ART/advait_config.json" > "$ART/advait_modified_config.json"

# 4. Config update, signed by both bank admins (the current Application
#    Admins policy is MAJORITY of 2, i.e. both). `peer channel update` adds
#    the submitting admin's signature (Org2) to Org1's.
createConfigUpdate "$CHANNEL" "$ART/advait_config.json" "$ART/advait_modified_config.json" "$ART/advait_update.pb" >/dev/null 2>&1
signConfigtxAsPeerOrg 1 "$ART/advait_update.pb" >/dev/null 2>&1
setGlobals 2 0 >/dev/null
peer channel update -o localhost:7050 --ordererTLSHostnameOverride orderer.example.com \
  -c "$CHANNEL" -f "$ART/advait_update.pb" --tls --cafile "$ORDERER_CA"

# 5. Verify from a freshly fetched config block.
sleep 3
fetchChannelConfig 1 0 "$CHANNEL" "$ART/advait_config_after.json" >/dev/null 2>&1
jq -e '.channel_group.groups.Application
  | (.groups | has("Org1MSP") and has("Org2MSP") and has("OracleMSP") and has("AuditorMSP"))
    and ([.policies.LifecycleEndorsement, .policies.Endorsement, .policies.Admins][] | .policy.type == 1)' \
  "$ART/advait_config_after.json" >/dev/null || { echo "ERROR: channel config does not show the new orgs/policies" >&2; exit 1; }
echo "Channel $CHANNEL members: $(jq -r '.channel_group.groups.Application.groups | keys | join(", ")' "$ART/advait_config_after.json")"
