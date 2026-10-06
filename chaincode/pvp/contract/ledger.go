package contract

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Balances is bank -> currency -> minor units.
type Balances map[string]map[string]int64

func (b Balances) get(bank, ccy string) int64 { return b[bank][ccy] }

func (b Balances) set(bank, ccy string, v int64) {
	if b[bank] == nil {
		b[bank] = map[string]int64{}
	}
	b[bank][ccy] = v
}

func (b Balances) clone() Balances {
	out := Balances{}
	for bank, m := range b {
		for ccy, v := range m {
			out.set(bank, ccy, v)
		}
	}
	return out
}

func key(stub shim.ChaincodeStubInterface, objectType string, attrs ...string) (string, error) {
	k, err := stub.CreateCompositeKey(objectType, attrs)
	if err != nil {
		return "", reject(ErrInternal, "composite key: %v", err)
	}
	return k, nil
}

func seqAttr(n int64) string { return fmt.Sprintf("%020d", n) }

// getJSON loads key into v. found=false if the key is absent.
func getJSON(stub shim.ChaincodeStubInterface, k string, v any) (bool, error) {
	raw, err := stub.GetState(k)
	if err != nil {
		return false, reject(ErrInternal, "read %q: %v", k, err)
	}
	if raw == nil {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, reject(ErrInternal, "decode %q: %v", k, err)
	}
	return true, nil
}

func putJSON(stub shim.ChaincodeStubInterface, k string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return reject(ErrInternal, "encode %q: %v", k, err)
	}
	if err := stub.PutState(k, raw); err != nil {
		return reject(ErrInternal, "write %q: %v", k, err)
	}
	return nil
}

func loadConfig(stub shim.ChaincodeStubInterface) (*Config, error) {
	k, err := key(stub, keyConfig)
	if err != nil {
		return nil, err
	}
	var cfg Config
	found, err := getJSON(stub, k, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, reject(ErrNotInitialized, "ledger is not initialised; call InitLedger first")
	}
	return &cfg, nil
}

// callerMSP is the MSP ID of the transaction submitter, as authenticated by
// Fabric from the signed proposal. It cannot be set by the caller.
func callerMSP(ctx contractapi.TransactionContextInterface) (string, error) {
	msp, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return "", reject(ErrUnauthorized, "cannot determine submitter MSP: %v", err)
	}
	return msp, nil
}

// callerBank checks if the submitter MSP is mapped to any bank role, returning the submitter MSP ID.
func callerBank(ctx contractapi.TransactionContextInterface, cfg *Config) (string, string, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return "", "", err
	}
	for _, b := range allBanks {
		if cfg.Banks[b] == msp {
			return b, msp, nil
		}
	}
	return "", msp, reject(ErrUnauthorized, "submitter MSP %q is not a settlement bank; only banks may move value", msp)
}

// authorizeBankCaller enforces explicit proxy authorization: the authenticated
// Fabric caller (MSP) must be a settlement bank and must match the configured proxy MSP for the logical ledger participant `asBank`.
func authorizeBankCaller(ctx contractapi.TransactionContextInterface, cfg *Config, asBank string) (string, error) {
	if !isBank(asBank) {
		return "", reject(ErrInvalidInput, "asBank must be a valid bank (%s, %s, %s, or %s), got %q", BankIN, BankFX, BankUS, BankSG, asBank)
	}
	_, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return "", err
	}
	authorizedMSP, ok := cfg.Banks[asBank]
	if !ok || authorizedMSP == "" || authorizedMSP != msp {
		return msp, reject(ErrForgedInstruction,
			"submitter %s is not authorized to act as bank %s (authorized MSP: %s); a bank cannot instruct for another bank",
			msp, asBank, authorizedMSP)
	}
	return msp, nil
}

// unpagedScanCap is the most rows a plain (unpaginated) range scan returns on
// Drunix's SQL state database: GetStateRangeScanIterator is a paginated scan
// with a fixed page size of 10, and anything after the 10th row is dropped
// silently. readAllBalances runs inside transactions that write, where Fabric
// forbids paginated queries, so it cannot page past this; it fails closed
// instead when a scan reaches the cap.
const unpagedScanCap = 10

// readAllBalances scans every BAL~ key on the ledger (committed state).
// Scanning, rather than reading a known list, means a stray account
// created by any means is still counted by the invariant.
//
// Drunix returns rows unordered and a row may repeat, so rows are counted by
// distinct key. A scan that returns unpagedScanCap rows may have been
// truncated, so the invariant refuses to run on it rather than sum a subset.
func readAllBalances(stub shim.ChaincodeStubInterface) (Balances, int, error) {
	it, err := stub.GetStateByPartialCompositeKey(keyBalance, []string{})
	if err != nil {
		return nil, 0, reject(ErrInternal, "scan balances: %v", err)
	}
	defer it.Close()
	out := Balances{}
	n := 0
	fetched := 0
	seen := map[string]bool{}
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, 0, reject(ErrInternal, "scan balances: %v", err)
		}
		if fetched++; fetched >= unpagedScanCap {
			return nil, 0, reject(ErrInternal,
				"balance scan reached %d rows, the state database's limit for an unpaginated scan; cannot prove every account was counted", unpagedScanCap)
		}
		if seen[kv.Key] {
			continue
		}
		seen[kv.Key] = true
		_, attrs, err := stub.SplitCompositeKey(kv.Key)
		if err != nil || len(attrs) != 2 {
			return nil, 0, reject(ErrInvariantViolation, "malformed balance key %q", kv.Key)
		}
		var v int64
		if err := json.Unmarshal(kv.Value, &v); err != nil {
			return nil, 0, reject(ErrInvariantViolation, "balance %q is not an integer", kv.Key)
		}
		out.set(attrs[0], attrs[1], v)
		n++
	}
	return out, n, nil
}

func readSupply(stub shim.ChaincodeStubInterface, ccy string) (int64, error) {
	k, err := key(stub, keySupply, ccy)
	if err != nil {
		return 0, err
	}
	var s int64
	found, err := getJSON(stub, k, &s)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, reject(ErrNotInitialized, "no supply recorded for %s", ccy)
	}
	return s, nil
}

// postState returns every balance on the ledger (committed state, found by
// scanning all BAL~ keys) with the current transaction's pending changes
// overlaid. Fabric does not let a transaction read its own writes, so the
// invariant must be evaluated on committed state + in-memory changes.
func postState(stub shim.ChaincodeStubInterface, post Balances) (Balances, error) {
	all, _, err := readAllBalances(stub)
	if err != nil {
		return nil, err
	}
	for bank, m := range post {
		for ccy, v := range m {
			all.set(bank, ccy, v)
		}
	}
	return all, nil
}

// conservationReport sums, per currency, every balance in `all` and compares
// the total with the supply fixed at InitLedger.
func conservationReport(stub shim.ChaincodeStubInterface, all Balances) ([]ConservationReport, error) {
	sums := map[string]*big.Int{}
	counts := map[string]int{}
	for _, m := range all {
		for ccy, v := range m {
			if sums[ccy] == nil {
				sums[ccy] = new(big.Int)
			}
			sums[ccy].Add(sums[ccy], big.NewInt(v))
			counts[ccy]++
		}
	}
	var reports []ConservationReport
	for _, ccy := range allCurrencies {
		supply, err := readSupply(stub, ccy)
		if err != nil {
			return nil, err
		}
		r := ConservationReport{Currency: ccy, Supply: supply, Accounts: counts[ccy]}
		if sum := sums[ccy]; sum == nil {
			r.Holds = supply == 0
		} else if sum.IsInt64() {
			r.Sum = sum.Int64()
			r.Holds = r.Sum == supply
		}
		reports = append(reports, r)
	}
	// A currency on the ledger with no recorded supply is itself a breach.
	for _, ccy := range sortedKeys(sums) {
		if ccy == INR || ccy == USD {
			continue
		}
		r := ConservationReport{Currency: ccy, Accounts: counts[ccy]}
		if sums[ccy].IsInt64() {
			r.Sum = sums[ccy].Int64()
		}
		reports = append(reports, r)
	}
	return reports, nil
}

// assertConservation rejects the transaction unless, after applying `post`,
// (1) every currency's total equals its fixed supply, and
// (2) no balance anywhere is negative.
func assertConservation(stub shim.ChaincodeStubInterface, post Balances) error {
	all, err := postState(stub, post)
	if err != nil {
		return err
	}
	reports, err := conservationReport(stub, all)
	if err != nil {
		return err
	}
	for _, r := range reports {
		if !r.Holds {
			return reject(ErrInvariantViolation,
				"value conservation breached for %s: ledger total would be %d but supply is %d; transaction refused",
				r.Currency, r.Sum, r.Supply)
		}
	}
	for _, bank := range sortedKeys(all) {
		for _, ccy := range sortedKeys(all[bank]) {
			if all[bank][ccy] < 0 {
				return reject(ErrInvariantViolation, "balance %s/%s would be negative (%d); transaction refused", bank, ccy, all[bank][ccy])
			}
		}
	}
	return nil
}

func writeBalances(stub shim.ChaincodeStubInterface, post Balances) error {
	for _, bank := range sortedKeys(post) {
		for _, ccy := range sortedKeys(post[bank]) {
			k, err := key(stub, keyBalance, bank, ccy)
			if err != nil {
				return err
			}
			if err := putJSON(stub, k, post[bank][ccy]); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func txTimestamp(stub shim.ChaincodeStubInterface) string {
	ts, err := stub.GetTxTimestamp()
	if err != nil || ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

// appendLog adds one entry to the append-only audit log.
func appendLog(stub shim.ChaincodeStubInterface, typ, submitterMSP, ref, detail string) error {
	hk, err := key(stub, keyLogHead)
	if err != nil {
		return err
	}
	var head int64
	if _, err := getJSON(stub, hk, &head); err != nil {
		return err
	}
	head++
	lk, err := key(stub, keyLog, seqAttr(head))
	if err != nil {
		return err
	}
	entry := LogEntry{
		N: head, Type: typ, TxID: stub.GetTxID(), SubmitterMSP: submitterMSP,
		TxTimestamp: txTimestamp(stub), Ref: ref, Detail: detail,
	}
	if err := putJSON(stub, lk, entry); err != nil {
		return err
	}
	return putJSON(stub, hk, head)
}
