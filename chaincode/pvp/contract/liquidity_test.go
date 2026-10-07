package contract

import (
	"encoding/json"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func (f *fixture) liquiditySettle(msp, batchID string, ids ...string) txResult {
	req := mustJSON(f.t, LiquidityRequest{BatchID: batchID, TradeIDs: ids})
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.LiquiditySettle(ctx, req) })
}

type previewLiquidityView struct {
	Plan         LiquidityPlan `json:"plan"`
	FundsOK      bool          `json:"fundsOk"`
	FundsMessage string        `json:"fundsMessage"`
}

func (f *fixture) previewLiquidity(batchID string, ids ...string) (previewLiquidityView, error) {
	f.t.Helper()
	req := mustJSON(f.t, LiquidityRequest{BatchID: batchID, TradeIDs: ids})
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.PreviewLiquidity(ctx, req)
	})
	var v previewLiquidityView
	if err == nil {
		if e := json.Unmarshal([]byte(out), &v); e != nil {
			f.t.Fatal(e)
		}
	}
	return v, err
}

func (f *fixture) withdraw(msp, tradeID, asBank string) txResult {
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error {
		return f.cc.WithdrawInstruction(ctx, tradeID, asBank)
	})
}

// TestLiquidity_ComputeMultiNet_IgnoresClientTotals verifies that the liquidity path
// recomputes positions directly from stored matched trades on-ledger.
func TestLiquidity_ComputeMultiNet_IgnoresClientTotals(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 10_000_00, 1)
	f.matched("T2", BankFX, 5_000_00, 1)

	pv, err := f.previewLiquidity("B_SAFE", "T1", "T2")
	if err != nil {
		t.Fatalf("PreviewLiquidity failed: %v", err)
	}

	// Verify gross total in USD is 15,000 USD (1,500,000 cents) recomputed from ledger state
	if got := pv.Plan.NetPlan.Gross[USD]; got != 15_000_00 {
		t.Fatalf("Gross USD = %d, want 1500000", got)
	}

	res := f.liquiditySettle(mspIN, "B_SAFE", "T1", "T2")
	f.mustOK(res)

	if f.tradeRecord("T1").Status != StatusSettled || f.tradeRecord("T2").Status != StatusSettled {
		t.Fatalf("trades T1 and T2 should be SETTLED")
	}
	f.mustInvariantHolds()
}

// TestLiquidity_GridlockResolution_RemovesSmallestSufficientTrade verifies the
// removal rule on BankFX's USD shortfall.
//
// CORRECTION (Stage 2): this test used to assert that the LARGEST trade, T_BIG,
// is dropped. That rule settled only 1.5m USD of the 3.0m USD batch. The rule
// now removes the smallest trade that alone covers the shortfall: BankFX is
// 1.0m USD short, T_MED (1.0m) covers it, so T_MED is dropped and T_BIG +
// T_SMALL (2.0m USD) settle, using all of BankFX's 2.0m USD.
func TestLiquidity_GridlockResolution_RemovesSmallestSufficientTrade(t *testing.T) {
	f := newFixtureWithRate(t)

	// BankFX holds 2,000,000 USD (openUSD_FX)
	// We create 3 trades where BankFX delivers USD:
	// T_BIG: 1,500,000 USD
	// T_MED: 1,000,000 USD
	// T_SMALL: 500,000 USD
	// Total USD required gross = 3,000,000 USD > 2,000,000 USD available.
	f.matched("T_BIG", BankFX, 1_500_000_00, 1)
	f.matched("T_MED", BankFX, 1_000_000_00, 1)
	f.matched("T_SMALL", BankFX, 500_000_00, 1)

	pv, err := f.previewLiquidity("B_GRID", "T_BIG", "T_MED", "T_SMALL")
	if err != nil {
		t.Fatalf("PreviewLiquidity error: %v", err)
	}

	if len(pv.Plan.DroppedTradeIDs) != 1 || pv.Plan.DroppedTradeIDs[0] != "T_MED" {
		t.Fatalf("Dropped trade = %v, want [T_MED]", pv.Plan.DroppedTradeIDs)
	}
	if got := pv.Plan.SettledTradeIDs; len(got) != 2 || got[0] != "T_BIG" || got[1] != "T_SMALL" {
		t.Fatalf("Settled trades = %v, want [T_BIG T_SMALL]", got)
	}
	want := Removal{Step: 1, TradeID: "T_MED", Bank: BankFX, Currency: USD, Shortfall: 1_000_000_00, Amount: 1_000_000_00}
	if len(pv.Plan.Removals) != 1 || pv.Plan.Removals[0] != want {
		t.Fatalf("Removals = %+v, want [%+v]", pv.Plan.Removals, want)
	}

	res := f.liquiditySettle(mspIN, "B_GRID", "T_BIG", "T_MED", "T_SMALL")
	f.mustOK(res)

	if f.tradeRecord("T_MED").Status != StatusMatched {
		t.Fatalf("T_MED should remain MATCHED, got %s", f.tradeRecord("T_MED").Status)
	}
	if f.tradeRecord("T_BIG").Status != StatusSettled || f.tradeRecord("T_SMALL").Status != StatusSettled {
		t.Fatalf("T_BIG and T_SMALL should be SETTLED")
	}
	if got := f.balances().Balances[BankFX][USD]; got != 0 {
		t.Fatalf("BankFX USD = %d, want 0 (all 2.0m USD delivered)", got)
	}
	f.mustInvariantHolds()
}

// TestLiquidity_GridlockResolution_TieBreaking verifies tie-breaking by currency then trade ID.
func TestLiquidity_GridlockResolution_TieBreaking(t *testing.T) {
	f := newFixtureWithRate(t)

	// Create 2 trades with identical amounts from BankFX: T01 and T02
	f.matched("T01", BankFX, 1_500_000_00, 1)
	f.matched("T02", BankFX, 1_500_000_00, 1)

	pv, err := f.previewLiquidity("B_TIE", "T01", "T02")
	if err != nil {
		t.Fatalf("PreviewLiquidity error: %v", err)
	}

	// Tie-break by trade ID: T01 comes before T02, so T01 is dropped.
	if len(pv.Plan.DroppedTradeIDs) != 1 || pv.Plan.DroppedTradeIDs[0] != "T01" {
		t.Fatalf("Dropped trade = %v, want [T01]", pv.Plan.DroppedTradeIDs)
	}
	if len(pv.Plan.SettledTradeIDs) != 1 || pv.Plan.SettledTradeIDs[0] != "T02" {
		t.Fatalf("Settled trade = %v, want [T02]", pv.Plan.SettledTradeIDs)
	}
}

// TestLiquidity_AtomicSettlement verifies that settled trades commit atomically together.
func TestLiquidity_AtomicSettlement(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 100_00, 1)
	f.matched("T2", BankFX, 200_00, 1)

	res := f.liquiditySettle(mspIN, "B_ATOMIC", "T1", "T2")
	f.mustOK(res)

	for _, id := range []string{"T1", "T2"} {
		tr := f.tradeRecord(id)
		if tr.Status != StatusSettled || tr.SettledVia != "LIQUIDITY:B_ATOMIC" {
			t.Fatalf("trade %s status = %s via %s, want SETTLED via LIQUIDITY:B_ATOMIC", id, tr.Status, tr.SettledVia)
		}
	}
	f.mustInvariantHolds()
}

// TestLiquidity_DuplicateTradesInRequest verifies rejection when request contains duplicate trade IDs.
func TestLiquidity_DuplicateTradesInRequest(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 100_00, 1)

	f.mustReject(ErrBatch, func() txResult {
		return f.liquiditySettle(mspIN, "B_DUP", "T1", "T1")
	})
}

// TestLiquidity_WithdrawnInstruction verifies instruction withdrawal for pending vs matched/settled trades.
func TestLiquidity_WithdrawnInstruction(t *testing.T) {
	f := newFixtureWithRate(t)

	// 1. Submit single-sided instruction (StatusPendingMatch)
	in1 := f.trade("W1", BankIN, BankFX, 100_00, 1)
	f.mustOK(f.instruct(mspIN, in1))

	tr1 := f.tradeRecord("W1")
	if tr1.Status != StatusPendingMatch {
		t.Fatalf("W1 status = %s, want PENDING_MATCH", tr1.Status)
	}

	// BankIN withdraws its pending instruction
	f.mustOK(f.withdraw(mspIN, "W1", BankIN))

	// GetTrade should now return not found / empty
	_, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.GetTrade(ctx, "W1")
	})
	if CodeOf(err) != ErrTradeNotFound {
		t.Fatalf("querying withdrawn trade W1: want ERR_TRADE_NOT_FOUND, got %v", err)
	}

	// 2. Matched trade cannot be withdrawn
	f.matched("W2", BankFX, 100_00, 1)
	f.mustReject(ErrAlreadyMatched, func() txResult {
		return f.withdraw(mspIN, "W2", BankIN)
	})

	// 3. Settled trade cannot be withdrawn
	f.mustOK(f.settle(mspIN, "W2"))
	f.mustReject(ErrAlreadySettled, func() txResult {
		return f.withdraw(mspIN, "W2", BankIN)
	})

	f.mustInvariantHolds()
}
