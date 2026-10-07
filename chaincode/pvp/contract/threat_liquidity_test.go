package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Threat tests for the liquidity engine, run against the real chaincode.
// Every rejection is checked with mustReject: the call is refused with the
// expected code, attempts no writes, and leaves committed state byte-for-byte
// unchanged; the value-conservation invariant is then re-checked.

func (f *fixture) liquiditySettleRaw(msp, raw string) txResult {
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.LiquiditySettle(ctx, raw) })
}

// A client cannot supply net positions, savings, a settled set or removals:
// the request is trade IDs only, and any other field is refused.
func TestThreatLiq_TamperedNetFieldsRefused(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 2_000_00, 1)
	for name, raw := range map[string]string{
		"net legs":       `{"batchId":"B1","tradeIds":["T1","T2"],"net":[{"currency":"USD","from":"BANKIN","to":"BANKFX","amount":1}]}`,
		"savings":        `{"batchId":"B1","tradeIds":["T1","T2"],"savingsPct":99}`,
		"settled set":    `{"batchId":"B1","tradeIds":["T1","T2"],"settledTradeIds":["T1"]}`,
		"removals":       `{"batchId":"B1","tradeIds":["T1","T2"],"removals":[]}`,
		"gross override": `{"batchId":"B1","tradeIds":["T1","T2"],"gross":{"USD":0}}`,
		"trailing data":  `{"batchId":"B1","tradeIds":["T1","T2"]}{"net":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f.mustReject(ErrInvalidInput, func() txResult { return f.liquiditySettleRaw(mspIN, raw) })
			_, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
				return f.cc.PreviewLiquidity(ctx, raw)
			})
			if CodeOf(err) != ErrInvalidInput {
				t.Fatalf("PreviewLiquidity: want %s, got %v", ErrInvalidInput, err)
			}
		})
	}
	f.mustInvariantHolds()
}

// A trade record whose INR leg was altered on the ledger (e.g. by modified
// chaincode on one peer) no longer matches its attested rate. The whole
// liquidity batch is refused, not just that trade.
func TestThreatLiq_TamperedTradeAmountRefusesBatch(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankFX, 2_000_00, 1)
	k, _ := shimKey(keyTrade, "T2")
	var tr Trade
	if err := json.Unmarshal(f.l.committed[k], &tr); err != nil {
		t.Fatal(err)
	}
	tr.INRAmount = tr.INRAmount / 2 // BankIN would pay half the INR
	f.l.committed[k] = []byte(mustJSON(t, tr))

	f.mustReject(ErrRateMismatch, func() txResult { return f.liquiditySettle(mspIN, "B1", "T1", "T2") })
	if st := f.tradeRecord("T1").Status; st != StatusMatched {
		t.Fatalf("T1 = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

// Underfunding that appears after a preview (another transaction spent the
// funds) is caught at settlement: LiquiditySettle recomputes against the
// balances it reads in its own transaction, so it drops more trades or
// settles nothing. It never overdraws.
func TestThreatLiq_HiddenUnderfundingAfterPreview(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("H0", BankFX, 1_000_000_00, 1) // settles on its own, spending 1.0m of BankFX's 2.0m USD
	f.matched("H1", BankFX, 1_500_000_00, 1)
	f.matched("H2", BankFX, 500_000_00, 1)

	pv, err := f.previewLiquidity("B_H", "H1", "H2")
	if err != nil {
		t.Fatal(err)
	}
	if !pv.FundsOK || len(pv.Plan.DroppedTradeIDs) != 0 {
		t.Fatalf("preview before the drain should settle both: %+v", pv)
	}

	f.mustOK(f.settle(mspFX, "H0"))

	// BankFX now holds 1.0m USD and is 1.0m short for H1+H2: H1 (1.5m) covers it.
	f.mustOK(f.liquiditySettle(mspIN, "B_H", "H1", "H2"))
	if st := f.tradeRecord("H1").Status; st != StatusMatched {
		t.Fatalf("H1 = %s, want MATCHED", st)
	}
	if st := f.tradeRecord("H2").Status; st != StatusSettled {
		t.Fatalf("H2 = %s, want SETTLED", st)
	}
	if got := f.balances().Balances[BankFX][USD]; got != 500_000_00 {
		t.Fatalf("BankFX USD = %d, want 50000000", got)
	}
	f.mustInvariantHolds()
}

func TestThreatLiq_HiddenUnderfunding_DrainedToGridlock(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("H0", BankFX, 2_000_000_00, 1) // spends all of BankFX's USD
	f.matched("H1", BankFX, 300_000_00, 1)
	f.matched("H2", BankFX, 200_000_00, 1)
	if pv, err := f.previewLiquidity("B_H", "H1", "H2"); err != nil || !pv.FundsOK {
		t.Fatalf("preview before the drain should be fundable: %v %+v", err, pv)
	}
	f.mustOK(f.settle(mspFX, "H0"))
	f.mustReject(ErrGridlock, func() txResult { return f.liquiditySettle(mspIN, "B_H", "H1", "H2") })
	f.mustInvariantHolds()
}

// A withdrawn instruction leaves no trade, and a one-sided trade cannot be
// pulled into a batch: both refuse the whole batch.
func TestThreatLiq_WithdrawnAndOneSidedInstructions(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("OK", BankFX, 100_00, 1)

	f.mustOK(f.instruct(mspIN, f.trade("W1", BankIN, BankFX, 100_00, 1)))
	f.mustOK(f.withdraw(mspIN, "W1", BankIN))
	f.mustReject(ErrTradeNotFound, func() txResult { return f.liquiditySettle(mspIN, "B1", "OK", "W1") })

	// BankFX instructs alone after the withdrawal: still one-sided.
	f.mustOK(f.instruct(mspFX, f.trade("W1", BankFX, BankFX, 100_00, 1)))
	f.mustReject(ErrUnilateral, func() txResult { return f.liquiditySettle(mspFX, "B1", "OK", "W1") })

	if st := f.tradeRecord("OK").Status; st != StatusMatched {
		t.Fatalf("OK = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

// A trade priced at a rate that has gone stale since it matched refuses the
// batch at settlement, even if the preview passed while it was fresh.
func TestThreatLiq_StaleInstructionAtSettlement(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("S1", BankFX, 100_00, 1)
	f.matched("S2", BankFX, 100_00, 1)
	if pv, err := f.previewLiquidity("B1", "S1", "S2"); err != nil || !pv.FundsOK {
		t.Fatalf("fresh preview: %v %+v", err, pv)
	}
	for seq := int64(2); seq <= 4; seq++ {
		f.mustOK(f.publish(mspOracle, signed(realOracle, seq, 83_250_000)))
	}
	f.mustReject(ErrAttestationStale, func() txResult { return f.liquiditySettle(mspIN, "B1", "S1", "S2") })
	f.mustInvariantHolds()
}

// Duplicates: the same ID twice in one request, a trade already settled by
// any path, and a batch ID already used by either NetSettle or LiquiditySettle.
func TestThreatLiq_DuplicateTradeAndBatch(t *testing.T) {
	f := newFixtureWithRate(t)
	for _, id := range []string{"D1", "D2", "D3", "D4", "D5"} {
		f.matched(id, BankFX, 100_00, 1)
	}
	f.mustReject(ErrBatch, func() txResult { return f.liquiditySettle(mspIN, "B1", "D1", "D2", "D1") })

	f.mustOK(f.liquiditySettle(mspIN, "B1", "D1", "D2"))
	f.mustReject(ErrReplay, func() txResult { return f.liquiditySettle(mspIN, "B1", "D3") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.liquiditySettle(mspIN, "B2", "D2", "D3") })

	f.mustOK(f.netSettle(mspIN, "N1", "D3", "D4"))
	f.mustReject(ErrReplay, func() txResult { return f.liquiditySettle(mspIN, "N1", "D5") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.liquiditySettle(mspIN, "B3", "D4", "D5") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.settle(mspIN, "D1") })

	if st := f.tradeRecord("D5").Status; st != StatusMatched {
		t.Fatalf("D5 = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

// Only a settlement bank's org may submit a liquidity settlement. The
// Auditor, the Oracle and unknown MSPs are refused; previews stay readable.
func TestThreatLiq_UnauthorizedCaller(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 100_00, 1)
	for _, msp := range []string{mspAuditor, mspOracle, "Org3MSP"} {
		t.Run(msp, func(t *testing.T) {
			err := f.mustReject(ErrUnauthorized, func() txResult { return f.liquiditySettle(msp, "B1", "T1") })
			if !strings.Contains(err.Error(), msp) {
				t.Fatalf("error should name the refused MSP: %v", err)
			}
		})
	}
	if _, err := f.previewLiquidity("B1", "T1"); err != nil {
		t.Fatalf("auditor preview should be allowed: %v", err)
	}
	if st := f.tradeRecord("T1").Status; st != StatusMatched {
		t.Fatalf("T1 = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

// A hostile BankFX runs every liquidity attack in a row. Each is refused with
// its own code, BankIN's balances never move, and the invariant holds after
// each step. The honest batch then settles.
func TestThreatLiq_MaliciousOrgScenario(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("M1", BankFX, 1_000_00, 1)
	f.matched("M2", BankFX, 2_000_00, 1)
	inBefore := f.balances().Balances[BankIN]

	attacks := []struct {
		code string
		call func() txResult
	}{
		{ErrInvalidInput, func() txResult {
			return f.liquiditySettleRaw(mspFX, `{"batchId":"X","tradeIds":["M1","M2"],"net":[]}`)
		}},
		{ErrSingleOrgTrade, func() txResult { return f.instruct(mspFX, f.trade4("M3", BankFX, BankFX, BankSG, 1_000_00, 1)) }},
		{ErrForgedInstruction, func() txResult { return f.instruct(mspFX, f.trade4("M4", BankIN, BankFX, BankIN, 1_000_00, 1)) }},
		{ErrTradeNotFound, func() txResult { return f.liquiditySettle(mspFX, "X", "M1", "M2", "GHOST") }},
		{ErrBatch, func() txResult { return f.liquiditySettle(mspFX, "X", "M1", "M1") }},
	}
	for i, a := range attacks {
		f.mustReject(a.code, a.call)
		if got := f.balances().Balances[BankIN]; got[INR] != inBefore[INR] || got[USD] != inBefore[USD] {
			t.Fatalf("attack %d moved BankIN's balances", i)
		}
		f.mustInvariantHolds()
	}
	f.mustOK(f.liquiditySettle(mspFX, "HONEST", "M1", "M2"))
	f.mustInvariantHolds()
}
