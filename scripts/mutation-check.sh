#!/usr/bin/env bash
# Mutation check: inject one realistic bug at a time into a COPY of the
# chaincode and confirm the unit tests fail. A mutation that survives means
# the tests do not actually protect that property.
set -u
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/chaincode/pvp"
WORK=$(mktemp -d)
pass=0; fail=0
mutate() { # name file perl-substitution
  local name="$1" file="$2" expr="$3"
  rm -rf "$WORK/pvp"; cp -r "$SRC" "$WORK/pvp"
  local before; before=$(md5sum "$WORK/pvp/contract/$file" | cut -d' ' -f1)
  perl -0pi -e "$expr" "$WORK/pvp/contract/$file"
  if [ "$(md5sum "$WORK/pvp/contract/$file" | cut -d' ' -f1)" = "$before" ]; then
    printf "  %-58s MUTATION DID NOT APPLY\n" "$name"; fail=$((fail+1)); return
  fi
  if (cd "$WORK/pvp" && go test ./contract/ -count=1 >/dev/null 2>&1); then
    printf "  %-58s SURVIVED (tests did not catch it)\n" "$name"; fail=$((fail+1))
  else
    printf "  %-58s killed\n" "$name"; pass=$((pass+1))
  fi
}
echo "Mutation check (each line = one injected bug):"
mutate "receiver never credited (value destroyed)"   contract.go 's/post\.set\(l\.To, l\.Ccy, to\)/_ = to/'
mutate "only the first leg is applied"                contract.go 's/for _, l := range legs \{\n\t\tfrom, err/for _, l := range legs[:1] {\n\t\tfrom, err/'
mutate "funds check removed"                          contract.go 's/if have := before\.get\(bank, ccy\); have < need\[bank\]\[ccy\]/if have := before.get(bank, ccy); false \&\& have < need[bank][ccy]/'
mutate "invariant check skipped in SettleTrade"       contract.go 's/if err := assertConservation\(stub, post\); err != nil \{\n\t\treturn err\n\t\}\n\n\t\/\/ Validation complete. Writes start here.\n\tif err := writeBalances/if false { return nil }\n\n\t\/\/ Validation complete. Writes start here.\n\tif err := writeBalances/'
mutate "invariant ignores negative balances"          ledger.go   's/if all\[bank\]\[ccy\] < 0 \{/if false \&\& all[bank][ccy] < 0 {/'
mutate "invariant sums a fixed list, not a full scan" ledger.go   's/stub\.GetStateByPartialCompositeKey\(keyBalance, \[\]string\{\}\)/stub.GetStateByPartialCompositeKey(keyBalance, []string{"BANK"})/'
mutate "signature verification bypassed"              attestation.go 's/if !ed25519\.Verify\(/if false \&\& !ed25519.Verify(/'
mutate "unsigned rates accepted"                      attestation.go 's/if strings\.TrimSpace\(a\.Signature\) == "" \{/if false {/'
mutate "forged-instruction check removed"             contract.go 's/if in\.AsBank != bank \{/if false {/'
mutate "double-settle allowed"                        contract.go 's/case StatusSettled:\n\t\treturn reject\(ErrAlreadySettled/case "never":\n\t\treturn reject(ErrAlreadySettled/'
mutate "settle with one instruction allowed"          contract.go 's/case StatusPendingMatch:\n\t\treturn reject/case "never":\n\t\treturn reject/'
mutate "replay of settled trade allowed"              contract.go 's/if trade != nil \&\& trade\.Status == StatusSettled \{/if false {/'
mutate "rate not re-checked at settlement"            contract.go 's/\/\/ Re-check the rate at settlement time.*?\n\tif _, err := checkRateUsable\(stub, cfg, trade\.RateSeq, trade\.USDAmount, trade\.INRAmount\); err != nil \{\n\t\treturn err\n\t\}/\/\/ (mutated)/s'
mutate "stale-rate window off by one"                 contract.go 's/if seq <= head-int64\(cfg\.RateWindow\)/if seq < head-int64(cfg.RateWindow)/'
mutate "out-of-band rate accepted"                    contract.go 's/if inr != expected \{/if false {/'
mutate "negative amounts accepted"                    money.go    's/if s\[0\] == .-. \{\n\t\treturn 0, reject/if false {\n\t\treturn 0, reject/'
mutate "amount cap removed"                           money.go    's/if v > MaxAmountMinor \{/if false {/'
mutate "rounding half-up changed to truncation"       money.go    's/p\.Add\(p, big\.NewInt\(RateScale\/2\)\)/_ = p/'
mutate "re-init allowed"                              contract.go 's/if existing != nil \{\n\t\treturn reject\(ErrAlreadyInitialized/if false {\n\t\treturn reject(ErrAlreadyInitialized/'
mutate "unknown JSON fields silently ignored"         contract.go 's/dec\.DisallowUnknownFields\(\)/_ = 0/'
mutate "non-bank submitter allowed to move value"     ledger.go   's/return "", msp, reject\(ErrUnauthorized/return BankIN, msp, nil; _ = reject(ErrUnauthorized/'
echo "killed: $pass   survived/not-applied: $fail"
rm -rf "$WORK"
[ "$fail" -eq 0 ]
