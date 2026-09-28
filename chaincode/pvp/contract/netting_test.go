package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func (f *fixture) netSettle(msp, batchID string, ids ...string) txResult {
	req := mustJSON(f.t, NetRequest{BatchID: batchID, TradeIDs: ids})
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.NetSettle(ctx, req) })
}

type previewView struct {
	Plan         NetPlan `json:"plan"`
	FundsOK      bool    `json:"fundsOk"`
	FundsMessage string  `json:"fundsMessage"`
}

func (f *fixture) previewNet(batchID string, ids ...string) (previewView, error) {
	f.t.Helper()
	req := mustJSON(f.t, NetRequest{BatchID: batchID, TradeIDs: ids})
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.PreviewNet(ctx, req) })
	var v previewView
	if err == nil {
		if e := json.Unmarshal([]byte(out), &v); e != nil {
			f.t.Fatal(e)
		}
	}
	return v, err
}

func netOf(p NetPlan, ccy string) NetLeg {
	for _, n := range p.Net {
		if n.Currency == ccy {
			return n
		}
	}
	return NetLeg{}
}

// Four trades in both directions: the net per currency is the difference of
// what each bank owes, computed here independently of the chaincode.
func TestNet_NetOfNTradesIsComputedAndSettledAtomically(t *testing.T) {
	f := newFixtureWithRate(t)
	// BankFX pays USD, BankIN pays INR (three trades)...
	inrA := f.matched("A", BankFX, 10_000_00, 1)
	inrB := f.matched("B", BankFX, 2_500_00, 1)
	inrC := f.matched("C", BankFX, 700_00, 1)
	// ...and one the other way, which BankIN could not fund gross (it holds no USD).
	inrD := f.matched("D", BankIN, 4_000_00, 1)

	fxUSD := int64(10_000_00 + 2_500_00 + 700_00)
	inUSD := int64(4_000_00)
	inINR := inrA + inrB + inrC
	fxINR := inrD
	wantUSD := fxUSD - inUSD // BankFX -> BankIN
	wantINR := inINR - fxINR // BankIN -> BankFX

	pv, err := f.previewNet("B1", "A", "B", "C", "D")
	if err != nil {
		t.Fatal(err)
	}
	if !pv.FundsOK {
		t.Fatalf("preview says funds are short: %s", pv.FundsMessage)
	}
	if g := pv.Plan.Gross; g[USD] != fxUSD+inUSD || g[INR] != inINR+fxINR {
		t.Fatalf("gross = %v, want USD %d INR %d", g, fxUSD+inUSD, inINR+fxINR)
	}
	if n := netOf(pv.Plan, USD); n.From != BankFX || n.To != BankIN || n.Amount != wantUSD {
		t.Fatalf("net USD = %+v, want BANKFX->BANKIN %d", n, wantUSD)
	}
	if n := netOf(pv.Plan, INR); n.From != BankIN || n.To != BankFX || n.Amount != wantINR {
		t.Fatalf("net INR = %+v, want BANKIN->BANKFX %d", n, wantINR)
	}

	before := f.balances().Balances
	res := f.netSettle(mspIN, "B1", "D", "A", "C", "B") // order does not matter
	f.mustOK(res)
	after := f.balances().Balances

	for bank, ccyDelta := range map[string]map[string]int64{
		BankIN: {USD: +wantUSD, INR: -wantINR},
		BankFX: {USD: -wantUSD, INR: +wantINR},
	} {
		for ccy, d := range ccyDelta {
			if got := after[bank][ccy] - before[bank][ccy]; got != d {
				t.Fatalf("%s %s moved %d, want exactly the net %d", bank, ccy, got, d)
			}
		}
	}
	for _, id := range []string{"A", "B", "C", "D"} {
		tr := f.tradeRecord(id)
		if tr.Status != StatusSettled || tr.SettledVia != "NET:B1" || tr.SettledTx != res.TxID {
			t.Fatalf("trade %s: status %s via %s tx %s; want SETTLED via NET:B1 in %s", id, tr.Status, tr.SettledVia, tr.SettledTx, res.TxID)
		}
	}
	log := f.auditLog()
	last := log[len(log)-1]
	if last.Type != "NET_SETTLED" || last.Ref != "B1" || last.TxID != res.TxID {
		t.Fatalf("last audit entry = %+v, want NET_SETTLED for B1", last)
	}
	f.mustInvariantHolds()

	// The batch record is readable and matches what settled.
	out, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetBatch(ctx, "B1") })
	if err != nil {
		t.Fatal(err)
	}
	var b Batch
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	if b.SettledTx != res.TxID || len(b.TradeIDs) != 4 || netOf(b.NetPlan, USD).Amount != wantUSD {
		t.Fatalf("batch record = %+v", b)
	}
}

// Netting funds what gross cannot: BankIN holds no USD, so the trade where it
// pays USD fails on its own, but inside a batch only the net must be funded.
func TestNet_SettlesABatchWhoseTradeCannotSettleGross(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("FX-PAYS", BankFX, 10_000_00, 1)
	f.matched("IN-PAYS", BankIN, 4_000_00, 1)
	f.mustReject(ErrInsufficientFunds, func() txResult { return f.settle(mspIN, "IN-PAYS") })
	f.mustOK(f.netSettle(mspIN, "B1", "FX-PAYS", "IN-PAYS"))
	f.mustInvariantHolds()
}

// One trade that cannot settle makes the whole batch fail, and nothing is
// written (mustReject checks no writes were attempted and state is identical).
func TestNet_FailingBatchWritesNothing(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 1_000_00, 1)
	f.mustOK(f.instruct(mspFX, f.trade("T3", BankFX, BankFX, 1_000_00, 1))) // one-sided

	f.mustReject(ErrUnilateral, func() txResult { return f.netSettle(mspIN, "B1", "T1", "T2", "T3") })
	f.mustReject(ErrTradeNotFound, func() txResult { return f.netSettle(mspIN, "B1", "T1", "NOPE") })
	for _, id := range []string{"T1", "T2"} {
		if s := f.tradeRecord(id).Status; s != StatusMatched {
			t.Fatalf("%s is %s after a refused batch, want MATCHED", id, s)
		}
	}
	f.mustInvariantHolds()
}

// Funds are checked against the net: a batch whose net exceeds the payer's
// balance is refused whole.
func TestNet_UnderfundedNetRejects(t *testing.T) {
	f := newFixtureWithRate(t)
	// BankFX holds 2,000,000.00 USD; two trades net to 3,000,000.00 USD from BankFX.
	f.matched("BIG1", BankFX, 1_500_000_00, 1)
	f.matched("BIG2", BankFX, 1_500_000_00, 1)
	pv, err := f.previewNet("B1", "BIG1", "BIG2")
	if err != nil {
		t.Fatal(err)
	}
	if pv.FundsOK || !strings.Contains(pv.FundsMessage, ErrInsufficientFunds) {
		t.Fatalf("preview should report the net shortfall, got ok=%v %q", pv.FundsOK, pv.FundsMessage)
	}
	f.mustReject(ErrInsufficientFunds, func() txResult { return f.netSettle(mspIN, "B1", "BIG1", "BIG2") })
	f.mustInvariantHolds()
}

// A batch that includes an already-settled trade is refused whole; the
// unsettled trade in it stays MATCHED.
func TestNet_BatchMixingSettledAndUnsettledRejects(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 1_000_00, 1)
	f.mustOK(f.settle(mspIN, "T1"))
	f.mustReject(ErrAlreadySettled, func() txResult { return f.netSettle(mspIN, "B1", "T1", "T2") })
	if s := f.tradeRecord("T2").Status; s != StatusMatched {
		t.Fatalf("T2 is %s, want MATCHED", s)
	}
	f.mustInvariantHolds()
}

// Settled-by-netting trades cannot settle again, gross or in another batch,
// and a batch ID cannot be replayed.
func TestNet_NoDoubleSettleOrReplay(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 1_000_00, 1)
	f.mustOK(f.netSettle(mspIN, "B1", "T1", "T2"))
	f.mustReject(ErrAlreadySettled, func() txResult { return f.settle(mspFX, "T1") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.netSettle(mspFX, "B2", "T1", "T2") })

	f.matched("T3", BankFX, 1_000_00, 1)
	f.matched("T4", BankFX, 1_000_00, 1)
	f.mustReject(ErrReplay, func() txResult { return f.netSettle(mspIN, "B1", "T3", "T4") })
	f.mustInvariantHolds()
}

func TestNet_BatchValidation(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 1_000_00, 1)
	f.mustReject(ErrBatch, func() txResult { return f.netSettle(mspIN, "B1", "T1") })
	f.mustReject(ErrBatch, func() txResult { return f.netSettle(mspIN, "B1", "T1", "T1") })
	f.mustReject(ErrInvalidInput, func() txResult { return f.netSettle(mspIN, "bad id", "T1", "T2") })
	// Only a settlement bank may net-settle.
	f.mustReject(ErrUnauthorized, func() txResult { return f.netSettle(mspAuditor, "B1", "T1", "T2") })
	// Unknown JSON fields are refused, like every other request.
	f.mustReject(ErrInvalidInput, func() txResult {
		return f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error {
			return f.cc.NetSettle(ctx, `{"batchId":"B1","tradeIds":["T1","T2"],"netOverride":0}`)
		})
	})
	f.mustInvariantHolds()
}

// Trades that net to zero in a currency move nothing in it, and still settle.
func TestNet_ExactOffsetMovesNothingButSettles(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("FWD", BankFX, 1_000_00, 1) // FX pays 1,000 USD; IN pays INR
	f.mustOK(f.netSettle(mspIN, "SEED", "FWD", mustMatch(f, "FWD2", BankFX, 1_000_00)))
	// Now BankIN holds USD and BankFX holds INR, so both directions are fundable.
	f.matched("X", BankFX, 1_000_00, 1)
	f.matched("Y", BankIN, 1_000_00, 1)
	before := f.balances().Balances
	pv, err := f.previewNet("B1", "X", "Y")
	if err != nil {
		t.Fatal(err)
	}
	if netOf(pv.Plan, USD).Amount != 0 || netOf(pv.Plan, INR).Amount != 0 {
		t.Fatalf("equal and opposite trades should net to zero, got %+v", pv.Plan.Net)
	}
	f.mustOK(f.netSettle(mspIN, "B1", "X", "Y"))
	if after := f.balances().Balances; after[BankIN][USD] != before[BankIN][USD] || after[BankFX][INR] != before[BankFX][INR] {
		t.Fatalf("a zero net moved balances: before %v after %v", before, after)
	}
	if f.tradeRecord("X").Status != StatusSettled || f.tradeRecord("Y").Status != StatusSettled {
		t.Fatal("both offsetting trades should be SETTLED")
	}
	f.mustInvariantHolds()
}

func mustMatch(f *fixture, id, usdDeliverer string, usd int64) string {
	f.matched(id, usdDeliverer, usd, 1)
	return id
}
