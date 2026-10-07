package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// The liquidity seed data (testdata/liquidity_seed.json) is what pvpctl seed
// instructs on the live network and what the frontend's scenarios list. This
// test replays it through the real chaincode, from the same opening balances
// as pvpctl init and the same 83.25 rate, and checks each scenario's outcome
// against figures worked out by hand from the trade definitions.

type seedFile struct {
	OpeningBalances map[string]map[string]string `json:"openingBalances"`
	Trades          []struct {
		TradeID      string `json:"tradeId"`
		USDDeliverer string `json:"usdDeliverer"`
		INRDeliverer string `json:"inrDeliverer"`
		USDAmount    string `json:"usdAmount"`
	} `json:"trades"`
	Scenarios []struct {
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Note     string   `json:"note"`
		TradeIDs []string `json:"tradeIds"`
	} `json:"scenarios"`
}

func loadSeed(t *testing.T) seedFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/liquidity_seed.json")
	if err != nil {
		t.Fatal(err)
	}
	var s seedFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&struct {
		Description string `json:"description"`
		*seedFile
	}{seedFile: &s}); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return s
}

func seededFixture(t *testing.T, s seedFile) *fixture {
	t.Helper()
	f, call := initWith(t, func(r *InitRequest) { r.Balances = s.OpeningBalances })
	f.mustOK(call())
	f.mustOK(f.publish(mspOracle, signed(realOracle, 1, 83_250_000)))
	for _, tr := range s.Trades {
		usd, err := strconv.ParseInt(tr.USDAmount, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		f.matched4(tr.TradeID, tr.USDDeliverer, tr.INRDeliverer, usd, 1)
	}
	return f
}

func TestSeed_Shape(t *testing.T) {
	s := loadSeed(t)
	if n := len(s.Trades); n < 12 || n > 20 {
		t.Fatalf("seed has %d trades, want 12-20", n)
	}
	inScenario := map[string]int{}
	for _, sc := range s.Scenarios {
		if sc.ID == "" || sc.Title == "" || len(sc.TradeIDs) == 0 {
			t.Fatalf("incomplete scenario %+v", sc)
		}
		for _, id := range sc.TradeIDs {
			inScenario[id]++
		}
	}
	seen := map[string]bool{}
	for _, tr := range s.Trades {
		if seen[tr.TradeID] {
			t.Fatalf("duplicate trade %s", tr.TradeID)
		}
		seen[tr.TradeID] = true
		if inScenario[tr.TradeID] != 1 {
			t.Fatalf("trade %s is in %d scenarios, want exactly 1", tr.TradeID, inScenario[tr.TradeID])
		}
	}
	if len(inScenario) != len(s.Trades) {
		t.Fatalf("scenarios name %d trades, seed defines %d", len(inScenario), len(s.Trades))
	}
}

func netLegsByCcy(p *NetPlan) map[string][]NetLeg {
	out := map[string][]NetLeg{}
	for _, n := range p.Net {
		if n.Amount > 0 {
			out[n.Currency] = append(out[n.Currency], n)
		}
	}
	return out
}

// Every scenario, run in order on one ledger as the demo does.
func TestSeed_ScenariosSettleAsExpected(t *testing.T) {
	s := loadSeed(t)
	f := seededFixture(t, s)
	scen := map[string][]string{}
	for _, sc := range s.Scenarios {
		scen[sc.ID] = sc.TradeIDs
	}

	// 1. Four-bank cycle, 400,000.00 USD on every leg: nets to zero in both currencies.
	pv, err := f.previewLiquidity("S1", scen["four-bank-cycle"]...)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.FundsOK || len(pv.Plan.SettledTradeIDs) != 4 || len(netLegsByCcy(pv.Plan.NetPlan)) != 0 {
		t.Fatalf("four-bank cycle: %+v", pv.Plan)
	}
	if len(pv.Plan.Cycles) != 2 {
		t.Fatalf("want one four-bank cycle per currency, got %+v", pv.Plan.Cycles)
	}
	for _, c := range pv.Plan.Cycles {
		if len(c.Path) != 5 {
			t.Fatalf("cycle %v is not a four-bank loop", c.Path)
		}
	}
	if pv.Plan.NetPlan.Gross[USD] != 160_000_000 {
		t.Fatalf("gross USD %d", pv.Plan.NetPlan.Gross[USD])
	}
	before := f.balances().Balances
	f.mustOK(f.liquiditySettle(mspIN, "S1", scen["four-bank-cycle"]...))
	if !equalBalances(before, f.balances().Balances) {
		t.Fatal("a cycle that nets to zero moved balances")
	}

	// 2. Four-bank cycle with a residual: 0.2m offsets around the loop; BankFX
	// pays BankIN the 0.1m USD residual and BankIN pays BankFX 8,325,000.00 INR.
	pv, err = f.previewLiquidity("S2", scen["four-bank-cycle-residual"]...)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]NetLeg{
		USD: {{Currency: USD, From: BankFX, To: BankIN, Amount: 10_000_000}},
		INR: {{Currency: INR, From: BankIN, To: BankFX, Amount: 832_500_000}},
	}
	if !pv.FundsOK || !reflect.DeepEqual(netLegsByCcy(pv.Plan.NetPlan), want) || pv.Plan.NetPlan.Gross[USD] != 110_000_000 {
		t.Fatalf("residual cycle: net %+v gross %v", netLegsByCcy(pv.Plan.NetPlan), pv.Plan.NetPlan.Gross)
	}
	f.mustOK(f.liquiditySettle(mspFX, "S2", scen["four-bank-cycle-residual"]...))

	// 3. Three-bank chain: BankFX owes 1.2m USD, split between BankIN (net 0.5m) and BankSG (0.7m).
	pv, err = f.previewLiquidity("S3", scen["three-bank-chain"]...)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string][]NetLeg{
		USD: {{Currency: USD, From: BankFX, To: BankIN, Amount: 50_000_000}, {Currency: USD, From: BankFX, To: BankSG, Amount: 70_000_000}},
		INR: {{Currency: INR, From: BankIN, To: BankFX, Amount: 4_162_500_000}, {Currency: INR, From: BankSG, To: BankFX, Amount: 5_827_500_000}},
	}
	if !pv.FundsOK || !reflect.DeepEqual(netLegsByCcy(pv.Plan.NetPlan), want) {
		t.Fatalf("three-bank chain: net %+v", netLegsByCcy(pv.Plan.NetPlan))
	}
	f.mustReject(ErrInsufficientFunds, func() txResult { return f.settle(mspIN, "LQ10") }) // gross: BankIN holds no USD
	f.mustOK(f.liquiditySettle(mspIN, "S3", scen["three-bank-chain"]...))

	// 4. Bilateral: 0.5m one way and 0.2m back net to 0.3m USD and 24,975,000.00 INR.
	pv, err = f.previewLiquidity("S4", scen["bilateral"]...)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string][]NetLeg{
		USD: {{Currency: USD, From: BankFX, To: BankIN, Amount: 30_000_000}},
		INR: {{Currency: INR, From: BankIN, To: BankFX, Amount: 2_497_500_000}},
	}
	if !pv.FundsOK || !reflect.DeepEqual(netLegsByCcy(pv.Plan.NetPlan), want) {
		t.Fatalf("bilateral: net %+v", netLegsByCcy(pv.Plan.NetPlan))
	}
	f.mustOK(f.liquiditySettle(mspFX, "S4", scen["bilateral"]...))

	// 5. Gridlock resolved: BankUS owes 1.8m USD and holds 1.0m; LQ13 (0.9m) is
	// the smallest trade that covers the 0.8m shortfall, so it is dropped.
	pv, err = f.previewLiquidity("S5", scen["gridlock-resolved"]...)
	if err != nil {
		t.Fatal(err)
	}
	wantRemoval := Removal{Step: 1, TradeID: "LQ13", Bank: BankUS, Currency: USD, Shortfall: 80_000_000, Amount: 90_000_000}
	if !pv.FundsOK || len(pv.Plan.Removals) != 1 || pv.Plan.Removals[0] != wantRemoval ||
		!reflect.DeepEqual(pv.Plan.SettledTradeIDs, []string{"LQ14", "LQ15"}) {
		t.Fatalf("gridlock resolved: %+v", pv.Plan)
	}
	f.mustOK(f.liquiditySettle(mspIN, "S5", scen["gridlock-resolved"]...))
	if st := f.tradeRecord("LQ13").Status; st != StatusMatched {
		t.Fatalf("LQ13 = %s, want MATCHED", st)
	}

	// 6. Total gridlock: nothing settles and nothing is written.
	pv, err = f.previewLiquidity("S6", scen["gridlock-total"]...)
	if err != nil {
		t.Fatal(err)
	}
	if pv.FundsOK || !pv.Plan.Gridlocked || len(pv.Plan.SettledTradeIDs) != 0 {
		t.Fatalf("total gridlock: %+v", pv)
	}
	f.mustReject(ErrGridlock, func() txResult { return f.liquiditySettle(mspIN, "S6", scen["gridlock-total"]...) })
	f.mustInvariantHoldsAny()
}

// mustInvariantHoldsAny checks conservation without assuming the default
// fixture's opening balances.
func (f *fixture) mustInvariantHoldsAny() {
	f.t.Helper()
	if inv := f.invariant(); !inv.Holds {
		f.t.Fatalf("value-conservation invariant does not hold: %+v", inv.Currencies)
	}
}

// A three-bank cycle in one currency needs a trade between two banks of the
// same org (IN and US, or FX and SG), because every other pair spans the two
// orgs. GATE 1 refuses that trade, so a three-bank cycle cannot be created.
func TestSeed_ThreeBankCycleIsImpossibleUnderGate1(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched4("K1", BankIN, BankFX, 100_00, 1) // USD IN -> FX
	f.matched4("K2", BankFX, BankUS, 100_00, 1) // USD FX -> US
	// Closing the loop needs USD US -> IN: both custodied by Org1.
	for _, side := range []string{BankUS, BankIN} {
		f.mustReject(ErrSingleOrgTrade, func() txResult {
			return f.instruct(mspIN, f.trade4("K3", side, BankUS, BankIN, 100_00, 1))
		})
	}
	_, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetTrade(ctx, "K3") })
	if CodeOf(err) != ErrTradeNotFound {
		t.Fatalf("K3 must not exist: %v", err)
	}
}
