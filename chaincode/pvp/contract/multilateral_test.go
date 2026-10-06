package contract

import (
	"testing"
)

// Test4Bank_ProxyAuthorization verifies that authenticated caller MSPs are restricted
// to their configured bank proxy mappings (e.g. Org1MSP for BankIN/BankUS, Org2MSP for BankFX/BankSG).
func Test4Bank_ProxyAuthorization(t *testing.T) {
	f := newFixtureWithRate(t)

	// Org1MSP (mspIN) is mapped to BankIN and BankUS.
	// It can instruct for BankUS.
	inUS := f.trade4("T_US_SG", BankUS, BankUS, BankSG, 100_00, 1)
	f.mustOK(f.instruct(mspIN, inUS))

	// Org2MSP (mspFX) is mapped to BankFX and BankSG.
	// It can instruct for BankSG.
	inSG := f.trade4("T_US_SG", BankSG, BankUS, BankSG, 100_00, 1)
	f.mustOK(f.instruct(mspFX, inSG))

	tr := f.tradeRecord("T_US_SG")
	if tr.Status != StatusMatched {
		t.Fatalf("T_US_SG status = %s, want MATCHED", tr.Status)
	}

	// Org1MSP (mspIN) attempts to instruct claiming asBank = BankSG (proxy mismatch).
	forgedSG := f.trade4("T_FORGE", BankSG, BankUS, BankSG, 50_00, 1)
	f.mustReject(ErrForgedInstruction, func() txResult {
		return f.instruct(mspIN, forgedSG)
	})

	// Org2MSP (mspFX) attempts to instruct claiming asBank = BankUS (proxy mismatch).
	forgedUS := f.trade4("T_FORGE", BankUS, BankUS, BankSG, 50_00, 1)
	f.mustReject(ErrForgedInstruction, func() txResult {
		return f.instruct(mspFX, forgedUS)
	})

	f.mustInvariantHolds()
}

// Test4Bank_MultilateralNetting verifies netting across 4 participants.
func Test4Bank_MultilateralNetting(t *testing.T) {
	f := newFixtureWithRate(t)

	// Trade 1: BankFX pays 10,000 USD to BankIN (BankIN pays INR)
	f.matched("T1", BankFX, 10_000_00, 1)

	// Trade 2: BankUS pays 4,000 USD to BankSG (BankSG pays INR)
	// BankUS instructed by Org1MSP (mspIN), BankSG instructed by Org2MSP (mspFX)
	f.mustOK(f.instruct(mspIN, f.trade4("T2", BankUS, BankUS, BankSG, 4_000_00, 1)))
	f.mustOK(f.instruct(mspFX, f.trade4("T2", BankSG, BankUS, BankSG, 4_000_00, 1)))

	pv, err := f.previewNet("B_MULTI", "T1", "T2")
	if err != nil {
		t.Fatal(err)
	}
	if !pv.FundsOK {
		t.Fatalf("preview funds not OK: %s", pv.FundsMessage)
	}

	res := f.netSettle(mspIN, "B_MULTI", "T1", "T2")
	f.mustOK(res)

	if f.tradeRecord("T1").Status != StatusSettled || f.tradeRecord("T2").Status != StatusSettled {
		t.Fatalf("trades not settled")
	}

	f.mustInvariantHolds()
}

// Test4Bank_CycleDetection verifies deterministic cycle detection for circular payments.
func Test4Bank_CycleDetection(t *testing.T) {
	f := newFixtureWithRate(t)

	// Build circular USD payment chain: BankIN -> BankFX -> BankUS -> BankIN
	// 1. BankIN pays USD to BankFX (BankFX pays INR)
	t1 := f.trade4("C1", BankIN, BankIN, BankFX, 1_000_00, 1)
	// 2. BankFX pays USD to BankUS (BankUS pays INR)
	t2 := f.trade4("C2", BankFX, BankFX, BankUS, 1_000_00, 1)
	// 3. BankUS pays USD to BankIN (BankIN pays INR)
	t3 := f.trade4("C3", BankUS, BankUS, BankIN, 1_000_00, 1)

	tradeObjs := []*Trade{
		{TradeID: "C1", USDDeliverer: t1.USDDeliverer, INRDeliverer: t1.INRDeliverer, USDAmount: 100000, INRAmount: 8325000},
		{TradeID: "C2", USDDeliverer: t2.USDDeliverer, INRDeliverer: t2.INRDeliverer, USDAmount: 100000, INRAmount: 8325000},
		{TradeID: "C3", USDDeliverer: t3.USDDeliverer, INRDeliverer: t3.INRDeliverer, USDAmount: 100000, INRAmount: 8325000},
	}

	cycles := DetectCycles(tradeObjs)
	if len(cycles) == 0 {
		t.Fatalf("expected cycle detection to find circular obligations, got none")
	}

	// Verify multilateral net position calculation results in 0 net transfers for circular trades
	plan, err := computeNet("B_CYCLE", tradeObjs)
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range plan.Net {
		if n.Amount != 0 {
			t.Fatalf("circular batch should net to 0, got leg %+v", n)
		}
	}
}
