package contract

import (
	"strings"
	"testing"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// GATE 1: no single Fabric org can be both sides of a trade.
//
// BankIN and BankFX are the two bank orgs. BankUS and BankSG are simulated
// ledger accounts custodied by those orgs (fixture: BankUS by mspIN, BankSG by
// mspFX). Every trade must span both orgs, so each trade needs an instruction
// from each org before it can match.

func initWith(t *testing.T, mutate func(*InitRequest)) (*fixture, func() txResult) {
	f := &fixture{t: t, l: newFakeLedger(), cc: &PvPContract{}}
	req := initRequest()
	mutate(&req)
	raw := mustJSON(t, req)
	return f, func() txResult {
		return f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, raw) })
	}
}

func TestGate1_InitRejectsSharedOrgForINandFX(t *testing.T) {
	cases := map[string]func(*InitRequest){
		"BankFX mapped to BankIN's MSP": func(r *InitRequest) { r.Config.Banks[BankFX] = mspIN },
		"BankIN mapped to BankFX's MSP": func(r *InitRequest) { r.Config.Banks[BankIN] = mspFX },
		"every bank mapped to one MSP": func(r *InitRequest) {
			for _, b := range allBanks {
				r.Config.Banks[b] = mspIN
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, call := initWith(t, mutate)
			err := f.mustReject(ErrInvalidInput, call)
			if !strings.Contains(err.Error(), "must be different MSPs") {
				t.Fatalf("rejected for the wrong reason: %v", err)
			}
		})
	}
}

func TestGate1_InitRejectsBankOutsideTheTwoOrgs(t *testing.T) {
	for _, b := range []string{BankUS, BankSG} {
		t.Run(b, func(t *testing.T) {
			f, call := initWith(t, func(r *InitRequest) { r.Config.Banks[b] = "Org3MSP" })
			err := f.mustReject(ErrInvalidInput, call)
			if !strings.Contains(err.Error(), "must be custodied by") {
				t.Fatalf("rejected for the wrong reason: %v", err)
			}
		})
	}
}

// Every same-org pairing, both directions, instructed from either side by the
// one org that custodies both: nothing is ever written, so there is no trade
// to settle and no balance moves.
func TestGate1_SingleOrgCannotMatchOrSettle(t *testing.T) {
	cases := []struct {
		usd, inr, msp string
	}{
		{BankIN, BankUS, mspIN},
		{BankUS, BankIN, mspIN},
		{BankFX, BankSG, mspFX},
		{BankSG, BankFX, mspFX},
	}
	for _, c := range cases {
		t.Run(c.usd+"_pays_USD_to_"+c.inr, func(t *testing.T) {
			f := newFixtureWithRate(t)
			before := f.balances().Balances
			id := "SOLO-" + c.usd + "-" + c.inr
			for _, as := range []string{c.usd, c.inr} {
				f.mustReject(ErrSingleOrgTrade, func() txResult {
					return f.instruct(c.msp, f.trade4(id, as, c.usd, c.inr, 1_000_00, 1))
				})
			}
			_, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
				return f.cc.GetTrade(ctx, id)
			})
			if CodeOf(err) != ErrTradeNotFound {
				t.Fatalf("trade %s should not exist, got %v", id, err)
			}
			f.mustReject(ErrTradeNotFound, func() txResult { return f.settle(c.msp, id) })
			f.mustReject(ErrTradeNotFound, func() txResult { return f.liquidityResolve(c.msp, "B-"+id, id) })
			if after := f.balances().Balances; !equalBalances(before, after) {
				t.Fatalf("balances moved: before %v after %v", before, after)
			}
			f.mustInvariantHolds()
		})
	}
}

// The cross-org version of the same trade does match and settle: the gate
// blocks single-org trades, not four-bank trades.
func TestGate1_CrossOrgFourBankTradeStillSettles(t *testing.T) {
	f := newFixtureWithRate(t)
	inr := f.quote(1_000_00, 1)
	f.mustOK(f.instruct(mspIN, f.trade4("X1", BankUS, BankUS, BankSG, 1_000_00, 1)))
	f.mustOK(f.instruct(mspFX, f.trade4("X1", BankSG, BankUS, BankSG, 1_000_00, 1)))
	f.mustOK(f.settle(mspFX, "X1"))
	b := f.balances().Balances
	if b[BankUS][USD] != openUSD_US-1_000_00 || b[BankSG][USD] != openUSD_SG+1_000_00 {
		t.Fatalf("USD leg wrong: %v", b)
	}
	if b[BankSG][INR] != openINR_SG-inr || b[BankUS][INR] != openINR_US+inr {
		t.Fatalf("INR leg wrong: %v", b)
	}
	f.mustInvariantHolds()
}

// A same-org MATCHED trade record that bypassed SubmitInstruction (e.g.
// written by an older chaincode version) is still refused by every
// settlement path.
func TestGate1_SettleTimeRecheck(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("OK1", BankFX, 1_000_00, 1)
	bad := Trade{
		TradeID: "BAD1", Status: StatusMatched,
		USDDeliverer: BankUS, INRDeliverer: BankIN,
		USDAmount: 1_000_00, INRAmount: f.quote(1_000_00, 1), RateSeq: 1,
		InstructedBy: []string{BankIN, BankUS},
	}
	k, err := shim.CreateCompositeKey(keyTrade, []string{bad.TradeID})
	if err != nil {
		t.Fatal(err)
	}
	f.l.committed[k] = []byte(mustJSON(t, bad))

	f.mustReject(ErrSingleOrgTrade, func() txResult { return f.settle(mspIN, "BAD1") })
	f.mustReject(ErrSingleOrgTrade, func() txResult { return f.netSettle(mspIN, "B-NET", "BAD1", "OK1") })
	f.mustReject(ErrSingleOrgTrade, func() txResult { return f.liquidityResolve(mspIN, "B-LIQ", "BAD1", "OK1") })
	if _, err := f.previewNet("B-NET", "BAD1", "OK1"); CodeOf(err) != ErrSingleOrgTrade {
		t.Fatalf("PreviewNet: want %s, got %v", ErrSingleOrgTrade, err)
	}
	if _, err := f.previewLiquidity("B-LIQ", "BAD1", "OK1"); CodeOf(err) != ErrSingleOrgTrade {
		t.Fatalf("PreviewLiquidity: want %s, got %v", ErrSingleOrgTrade, err)
	}
	f.mustInvariantHolds()
}

// Four banks x two currencies: after a four-bank netting batch every one of
// the 8 accounts is accounted for and each currency still sums to supply.
func TestInvariant_CoversFourBanksTwoCurrencies(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("N1", BankFX, 10_000_00, 1)
	f.mustOK(f.instruct(mspIN, f.trade4("N2", BankUS, BankUS, BankSG, 4_000_00, 1)))
	f.mustOK(f.instruct(mspFX, f.trade4("N2", BankSG, BankUS, BankSG, 4_000_00, 1)))
	f.mustOK(f.netSettle(mspFX, "B4", "N1", "N2"))
	inv := f.invariant()
	for _, c := range inv.Currencies {
		if c.Accounts != len(allBanks) {
			t.Fatalf("%s: invariant summed %d accounts, want %d", c.Currency, c.Accounts, len(allBanks))
		}
	}
	b := f.balances().Balances
	for _, bank := range allBanks {
		for _, ccy := range allCurrencies {
			if _, ok := b[bank][ccy]; !ok {
				t.Fatalf("balance %s/%s missing from GetBalances", bank, ccy)
			}
		}
	}
	f.mustInvariantHolds()
}
