package contract

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// These tests cover how list queries behave on Drunix's SQL state database,
// which the fake ledger reproduces: a plain range scan returns at most 10
// rows, rows come back in no particular order, and Fabric forbids paginated
// queries in a transaction that writes. Before scan used a paginated query,
// GetTrades and GetAuditLog silently returned 10 unordered rows on the live
// network.

// More than 10 trades, rates and log entries: every list query returns all of
// them, in order.
func TestLists_ReturnEverythingInOrderPastTheScanCap(t *testing.T) {
	f := newFixtureWithRate(t)
	const nTrades = 14
	for i := 1; i <= nTrades; i++ {
		f.matched(fmt.Sprintf("T%02d", i), BankFX, 100_00, 1)
	}
	const nRates = 12 // seq 1 from the fixture, then 2..12
	for seq := int64(2); seq <= nRates; seq++ {
		f.mustOK(f.publish(mspIN, signed(realOracle, seq, 83_250_000+seq)))
	}

	var trades []Trade
	f.queryJSON(func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetTrades(ctx) }, &trades)
	if len(trades) != nTrades {
		t.Fatalf("GetTrades returned %d trades, want %d", len(trades), nTrades)
	}
	for i, tr := range trades {
		if want := fmt.Sprintf("T%02d", i+1); tr.TradeID != want {
			t.Fatalf("GetTrades[%d] = %s, want %s (key order)", i, tr.TradeID, want)
		}
	}

	var rates struct {
		Head  int64        `json:"head"`
		Rates []RateRecord `json:"rates"`
	}
	f.queryJSON(func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetRates(ctx) }, &rates)
	if rates.Head != nRates || len(rates.Rates) != nRates {
		t.Fatalf("GetRates: head %d, %d rates; want %d and %d", rates.Head, len(rates.Rates), nRates, nRates)
	}
	for i, r := range rates.Rates {
		if r.Seq != int64(i+1) {
			t.Fatalf("GetRates[%d].seq = %d, want %d", i, r.Seq, i+1)
		}
	}

	// INIT + 12 RATE_PUBLISHED + 2 INSTRUCTED per trade.
	log := f.auditLog()
	if want := 1 + nRates + 2*nTrades; len(log) != want {
		t.Fatalf("GetAuditLog returned %d entries, want %d", len(log), want)
	}
	for i, e := range log {
		if e.N != int64(i+1) {
			t.Fatalf("GetAuditLog[%d].n = %d, want %d (append order)", i, e.N, i+1)
		}
	}
}

// The invariant sums balances with a plain scan, because it runs inside
// transactions that write and those may not use paginated queries. If that
// scan reaches the database's cap it may be truncated, so settlement and the
// invariant check refuse rather than sum a subset.
func TestInvariant_FailsClosedWhenBalanceScanMayBeTruncated(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 100_00, 1)

	// 4 real accounts + 6 stray zero-balance accounts = 10 BAL~ keys, the cap.
	// Zero balances keep every sum correct, so only the cap can trigger this.
	for i := 0; i < 6; i++ {
		k, err := shim.CreateCompositeKey(keyBalance, []string{fmt.Sprintf("STRAY%d", i), USD})
		if err != nil {
			t.Fatal(err)
		}
		f.l.committed[k] = []byte("0")
	}

	f.mustReject(ErrInternal, func() txResult { return f.settle(mspIN, "T1") })
	_, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.CheckInvariant(ctx) })
	if CodeOf(err) != ErrInternal {
		t.Fatalf("CheckInvariant with a possibly truncated scan: want %s, got %v", ErrInternal, err)
	}
}

// One key short of the cap is still a complete scan: settlement works.
func TestInvariant_ScanBelowCapStillSettles(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 100_00, 1)
	for i := 0; i < 5; i++ { // 4 + 5 = 9 keys
		k, err := shim.CreateCompositeKey(keyBalance, []string{fmt.Sprintf("STRAY%d", i), USD})
		if err != nil {
			t.Fatal(err)
		}
		f.l.committed[k] = []byte("0")
	}
	f.mustOK(f.settle(mspIN, "T1"))
	f.mustInvariantHolds()
}

func (f *fixture) queryJSON(fn func(ctx contractapi.TransactionContextInterface) (string, error), v any) {
	f.t.Helper()
	out, err := f.l.query(f.t, mspAuditor, fn)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		f.t.Fatal(err)
	}
}
