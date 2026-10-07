package contract

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// custodian is the fixture MSP that instructs for a ledger bank.
func custodian(bank string) string {
	if bank == BankIN || bank == BankUS {
		return mspIN
	}
	return mspFX
}

// matched4 matches a trade between any two cross-org ledger banks, each side
// instructed by its own custodian org.
func (f *fixture) matched4(id, usdDeliverer, inrDeliverer string, usd, seq int64) {
	f.t.Helper()
	f.mustOK(f.instruct(custodian(usdDeliverer), f.trade4(id, usdDeliverer, usdDeliverer, inrDeliverer, usd, seq)))
	f.mustOK(f.instruct(custodian(inrDeliverer), f.trade4(id, inrDeliverer, usdDeliverer, inrDeliverer, usd, seq)))
}

func (f *fixture) previewLiquidityRaw(batchID string, ids ...string) string {
	f.t.Helper()
	req := mustJSON(f.t, LiquidityRequest{BatchID: batchID, TradeIDs: ids})
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.PreviewLiquidity(ctx, req)
	})
	if err != nil {
		f.t.Fatalf("PreviewLiquidity: %v", err)
	}
	return out
}

func (f *fixture) batchRecord(id string) Batch {
	f.t.Helper()
	var b Batch
	f.queryJSON(func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetBatch(ctx, id) }, &b)
	return b
}

// ---------------------------------------------------------------------------
// Pure resolver tests (resolveGridlock reads and writes nothing).

func tr(id, usdDeliverer, inrDeliverer string, usd, inr int64) *Trade {
	return &Trade{TradeID: id, Status: StatusMatched, USDDeliverer: usdDeliverer, INRDeliverer: inrDeliverer, USDAmount: usd, INRAmount: inr}
}

func bal(pairs ...any) Balances {
	b := Balances{}
	for _, bank := range allBanks {
		for _, c := range allCurrencies {
			b.set(bank, c, 0)
		}
	}
	for i := 0; i < len(pairs); i += 3 {
		b.set(pairs[i].(string), pairs[i+1].(string), int64(pairs[i+2].(int)))
	}
	return b
}

func removalIDs(p *LiquidityPlan) []string {
	ids := make([]string, len(p.Removals))
	for i, r := range p.Removals {
		ids[i] = r.TradeID
	}
	return ids
}

// An INR shortfall is resolved the same way as a USD one.
func TestResolver_INRShortfall_RemovesSmallestSufficient(t *testing.T) {
	trades := []*Trade{
		tr("T1", BankFX, BankIN, 1, 60),
		tr("T2", BankFX, BankIN, 1, 50),
		tr("T3", BankFX, BankIN, 1, 30),
	}
	// BankIN pays 140 INR, holds 100: short 40. T1 (60) and T2 (50) each cover it; T2 is smaller.
	plan, _, err := resolveGridlock("B", trades, bal(BankIN, INR, 100, BankFX, USD, 10))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.DroppedTradeIDs, []string{"T2"}) || !reflect.DeepEqual(plan.SettledTradeIDs, []string{"T1", "T3"}) {
		t.Fatalf("dropped %v settled %v, want [T2] and [T1 T3]", plan.DroppedTradeIDs, plan.SettledTradeIDs)
	}
	if r := plan.Removals[0]; r.Bank != BankIN || r.Currency != INR || r.Shortfall != 40 || r.Amount != 50 {
		t.Fatalf("removal %+v", r)
	}
}

// When no single trade covers the shortfall, the largest is removed first.
// This documents that the rule is greedy, not optimal: here it settles
// 1.5m USD, while dropping L3+L4 instead would have settled 1.7m USD.
func TestResolver_NoSingleTradeCovers_RemovesLargestThenSmallestSufficient(t *testing.T) {
	trades := []*Trade{
		tr("L1", BankFX, BankIN, 900_000_00, 1),
		tr("L2", BankFX, BankIN, 800_000_00, 1),
		tr("L3", BankFX, BankIN, 700_000_00, 1),
		tr("L4", BankFX, BankIN, 600_000_00, 1),
	}
	plan, _, err := resolveGridlock("B", trades, bal(BankFX, USD, 2_000_000_00, BankIN, INR, 100))
	if err != nil {
		t.Fatal(err)
	}
	// Short 1.0m: none covers, remove L1 (0.9m). Short 0.1m: L2, L3, L4 cover, remove L4 (smallest).
	if got := removalIDs(plan); !reflect.DeepEqual(got, []string{"L1", "L4"}) {
		t.Fatalf("removal order %v, want [L1 L4]", got)
	}
	if !reflect.DeepEqual(plan.SettledTradeIDs, []string{"L2", "L3"}) {
		t.Fatalf("settled %v, want [L2 L3]", plan.SettledTradeIDs)
	}
}

// Equal shortfalls: the earlier bank in allBanks is resolved first.
func TestResolver_EqualShortfalls_FixedBankOrder(t *testing.T) {
	trades := []*Trade{
		tr("X", BankFX, BankIN, 10, 1), // BankFX pays 10 USD
		tr("Y", BankUS, BankSG, 10, 1), // BankUS pays 10 USD
	}
	// BankIN and BankSG can pay their INR. BankFX and BankUS are both short
	// exactly 10 USD; BANKFX precedes BANKUS in allBanks, so X goes first.
	plan, post, err := resolveGridlock("B", trades, bal(BankIN, INR, 1, BankSG, INR, 1))
	if CodeOf(err) != ErrGridlock || post != nil {
		t.Fatalf("want ERR_GRIDLOCK and no post-state, got %v / %v", err, post)
	}
	if got := removalIDs(plan); !reflect.DeepEqual(got, []string{"X", "Y"}) {
		t.Fatalf("removal order %v, want [X Y]", got)
	}
}

// Removing a trade can make its counterparty short in the other currency;
// the next pass handles that, and here it ends in gridlock.
func TestResolver_CascadeEndsInGridlock(t *testing.T) {
	trades := []*Trade{
		tr("A", BankFX, BankIN, 100, 50), // FX pays 100 USD to IN, IN pays 50 INR to FX
		tr("B", BankIN, BankSG, 100, 70), // IN pays 100 USD to SG, SG pays 70 INR to IN
	}
	// Only FX has funds (100 USD). SG is short 70 INR -> remove B. Then IN is
	// short 50 INR (B's INR inflow is gone) -> remove A. Nothing is left.
	plan, post, err := resolveGridlock("B1", trades, bal(BankFX, USD, 100))
	if CodeOf(err) != ErrGridlock || post != nil {
		t.Fatalf("want ERR_GRIDLOCK and no post-state, got %v", err)
	}
	if !plan.Gridlocked || len(plan.SettledTradeIDs) != 0 || plan.NetPlan != nil {
		t.Fatalf("gridlocked plan must settle nothing: %+v", plan)
	}
	if got := removalIDs(plan); !reflect.DeepEqual(got, []string{"B", "A"}) {
		t.Fatalf("removal order %v, want [B A]", got)
	}
	if !strings.Contains(err.Error(), "nothing settled") {
		t.Fatalf("gridlock error must say nothing settled: %v", err)
	}
}

// Randomised property test with a fixed seed (reproducible). For every batch:
// the result partitions the input; removals <= trades; a resolved batch's net
// post-state equals applying every settled trade gross, is non-negative and
// conserves each currency; a gridlocked batch settles nothing; and shuffling
// the input order (and Go's per-run map randomisation) never changes the result.
func TestResolver_Properties(t *testing.T) {
	rng := rand.New(rand.NewSource(20261007))
	pairs := [][2]string{
		{BankIN, BankFX}, {BankIN, BankSG}, {BankUS, BankFX}, {BankUS, BankSG},
		{BankFX, BankIN}, {BankFX, BankUS}, {BankSG, BankIN}, {BankSG, BankUS},
	}
	gridlocked, partial, full := 0, 0, 0
	for c := 0; c < 400; c++ {
		n := 1 + rng.Intn(14)
		trades := make([]*Trade, n)
		for i := range trades {
			p := pairs[rng.Intn(len(pairs))]
			trades[i] = tr(string(rune('A'+i))+"-"+string(rune('a'+rng.Intn(26))), p[0], p[1], 1+rng.Int63n(1000), 1+rng.Int63n(80_000))
		}
		before := bal()
		for _, b := range allBanks {
			for _, ccy := range allCurrencies {
				if rng.Intn(3) > 0 {
					before.set(b, ccy, rng.Int63n(4_000)*int64(map[string]int{USD: 1, INR: 80}[ccy]))
				}
			}
		}

		plan, post, err := resolveGridlock("P", trades, before)
		if err != nil && CodeOf(err) != ErrGridlock {
			t.Fatalf("case %d: unexpected error %v", c, err)
		}

		// Partition and bound.
		all := append(append([]string{}, plan.SettledTradeIDs...), plan.DroppedTradeIDs...)
		sort.Strings(all)
		if !reflect.DeepEqual(all, plan.InputTradeIDs) || len(plan.Removals) != len(plan.DroppedTradeIDs) || len(plan.Removals) > n {
			t.Fatalf("case %d: bad partition %+v", c, plan)
		}
		for _, r := range plan.Removals {
			var rt *Trade
			for _, x := range trades {
				if x.TradeID == r.TradeID {
					rt = x
				}
			}
			if payerLeg(rt, r.Bank, r.Currency) != r.Amount || r.Amount <= 0 || r.Shortfall <= 0 {
				t.Fatalf("case %d: removal %+v does not match its trade", c, r)
			}
		}

		if err != nil {
			gridlocked++
			if !plan.Gridlocked || post != nil || len(plan.SettledTradeIDs) != 0 || plan.NetPlan != nil {
				t.Fatalf("case %d: gridlock must settle nothing: %+v", c, plan)
			}
		} else {
			if len(plan.DroppedTradeIDs) > 0 {
				partial++
			} else {
				full++
			}
			got := before.clone()
			for _, b := range sortedKeys(post) {
				for _, ccy := range sortedKeys(post[b]) {
					got.set(b, ccy, post[b][ccy])
				}
			}
			want := before.clone()
			settled := map[string]bool{}
			for _, id := range plan.SettledTradeIDs {
				settled[id] = true
			}
			for _, x := range trades {
				if settled[x.TradeID] {
					want.set(x.USDDeliverer, USD, want.get(x.USDDeliverer, USD)-x.USDAmount)
					want.set(x.INRDeliverer, USD, want.get(x.INRDeliverer, USD)+x.USDAmount)
					want.set(x.INRDeliverer, INR, want.get(x.INRDeliverer, INR)-x.INRAmount)
					want.set(x.USDDeliverer, INR, want.get(x.USDDeliverer, INR)+x.INRAmount)
				}
			}
			if !reflect.DeepEqual(snapshot(got), snapshot(want)) {
				t.Fatalf("case %d: net post-state %v != gross application %v", c, snapshot(got), snapshot(want))
			}
			for _, ccy := range allCurrencies {
				var sb, sa int64
				for _, b := range allBanks {
					sb += before.get(b, ccy)
					sa += got.get(b, ccy)
					if got.get(b, ccy) < 0 {
						t.Fatalf("case %d: %s/%s negative", c, b, ccy)
					}
				}
				if sb != sa {
					t.Fatalf("case %d: %s not conserved", c, ccy)
				}
			}
		}

		// Determinism: any input order gives the identical plan and post-state.
		ref, _ := json.Marshal(plan)
		for s := 0; s < 4; s++ {
			sh := append([]*Trade(nil), trades...)
			rng.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
			p2, post2, _ := resolveGridlock("P", sh, before)
			again, _ := json.Marshal(p2)
			if string(again) != string(ref) || !reflect.DeepEqual(snapshot(post2), snapshot(post)) {
				t.Fatalf("case %d: result depends on input order:\n%s\n%s", c, ref, again)
			}
		}
	}
	t.Logf("400 cases: %d settled in full, %d partially, %d gridlocked", full, partial, gridlocked)
	if gridlocked == 0 || partial == 0 || full == 0 {
		t.Fatalf("generator must cover all three outcomes (full %d, partial %d, gridlocked %d)", full, partial, gridlocked)
	}
}

// ---------------------------------------------------------------------------
// On-chain tests (real chaincode against the fake ledger).

// A gridlocked batch is refused with nothing written; Preview reports it.
func TestLiquidity_GridlockedBatchSettlesNothing(t *testing.T) {
	f := newFixtureWithRate(t)
	// Each trade alone needs 2.5m USD from BankFX, which holds 2.0m.
	f.matched("G1", BankFX, 2_500_000_00, 1)
	f.matched("G2", BankFX, 2_500_000_00, 1)

	pv, err := f.previewLiquidity("B_GL", "G1", "G2")
	if err != nil {
		t.Fatal(err)
	}
	if pv.FundsOK || !pv.Plan.Gridlocked || len(pv.Plan.SettledTradeIDs) != 0 || len(pv.Plan.Removals) != 2 {
		t.Fatalf("preview must report gridlock with 2 removals: %+v", pv)
	}

	before := f.balances().Balances
	f.mustReject(ErrGridlock, func() txResult { return f.liquiditySettle(mspIN, "B_GL", "G1", "G2") })
	for _, id := range []string{"G1", "G2"} {
		if st := f.tradeRecord(id).Status; st != StatusMatched {
			t.Fatalf("%s = %s, want MATCHED", id, st)
		}
	}
	if !equalBalances(before, f.balances().Balances) {
		t.Fatal("balances moved on a gridlocked batch")
	}
	// The batch ID was not consumed: nothing was written.
	f.mustReject(ErrBatch, func() txResult {
		return f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error {
			_, err := f.cc.GetBatch(ctx, "B_GL")
			return err
		})
	})
	f.mustInvariantHolds()
}

// Only funding shortfalls drop trades. Any other bad trade refuses the whole
// batch, even though the remaining trades would be fundable.
func TestLiquidity_InvalidTradeRefusesWholeBatch(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		setup func(f *fixture) []string
	}{
		{"unknown trade", ErrTradeNotFound, func(f *fixture) []string {
			f.matched("OK", BankFX, 100_00, 1)
			return []string{"OK", "NOPE"}
		}},
		{"one-sided trade", ErrUnilateral, func(f *fixture) []string {
			f.matched("OK", BankFX, 100_00, 1)
			f.mustOK(f.instruct(mspIN, f.trade("HALF", BankIN, BankFX, 100_00, 1)))
			return []string{"OK", "HALF"}
		}},
		{"already settled trade", ErrAlreadySettled, func(f *fixture) []string {
			f.matched("OK", BankFX, 100_00, 1)
			f.matched("DONE", BankFX, 100_00, 1)
			f.mustOK(f.settle(mspIN, "DONE"))
			return []string{"OK", "DONE"}
		}},
		{"stale rate", ErrAttestationStale, func(f *fixture) []string {
			f.matched("OLD", BankFX, 100_00, 1)
			for seq := int64(2); seq <= 4; seq++ {
				f.mustOK(f.publish(mspOracle, signed(realOracle, seq, 83_250_000)))
			}
			f.matched("OK", BankFX, 100_00, 4)
			return []string{"OK", "OLD"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixtureWithRate(t)
			ids := c.setup(f)
			f.mustReject(c.code, func() txResult { return f.liquiditySettle(mspIN, "B_BAD", ids...) })
			if st := f.tradeRecord("OK").Status; st != StatusMatched {
				t.Fatalf("OK = %s, want MATCHED", st)
			}
			f.mustInvariantHolds()
		})
	}
}

// A four-bank circular batch nets to zero in both currencies: it settles
// with no balance moving, although no trade could settle gross on its own.
func TestLiquidity_FourBankCycleSettlesWithZeroMovement(t *testing.T) {
	f := newFixtureWithRate(t)
	// USD: IN -> FX -> US -> SG -> IN. INR flows the other way. Every pair spans both orgs.
	f.matched4("C1", BankIN, BankFX, 1_000_00, 1)
	f.matched4("C2", BankFX, BankUS, 1_000_00, 1)
	f.matched4("C3", BankUS, BankSG, 1_000_00, 1)
	f.matched4("C4", BankSG, BankIN, 1_000_00, 1)

	// Gross, C1 cannot settle: BankIN holds no USD and BankFX holds no INR.
	f.mustReject(ErrInsufficientFunds, func() txResult { return f.settle(mspIN, "C1") })

	before := f.balances().Balances
	f.mustOK(f.liquiditySettle(mspFX, "B_CYC", "C4", "C2", "C3", "C1"))
	if !equalBalances(before, f.balances().Balances) {
		t.Fatal("a fully circular batch must move no balance")
	}
	b := f.batchRecord("B_CYC")
	for _, n := range b.Net {
		if n.Amount != 0 {
			t.Fatalf("net leg %+v, want zero", n)
		}
	}
	if b.Liquidity == nil || len(b.Liquidity.DroppedTradeIDs) != 0 || len(b.Liquidity.Cycles) != 2 {
		t.Fatalf("want no drops and one cycle per currency, got %+v", b.Liquidity)
	}
	for _, id := range []string{"C1", "C2", "C3", "C4"} {
		if tr := f.tradeRecord(id); tr.Status != StatusSettled || tr.SettledVia != "LIQUIDITY:B_CYC" {
			t.Fatalf("%s = %s via %s", id, tr.Status, tr.SettledVia)
		}
	}
	f.mustInvariantHolds()
}

// Settlement is one transaction: the settled trades, every balance and the
// batch record are written together; dropped trades are not written at all.
// The preview predicts exactly what settles, and the batch ID cannot be reused.
func TestLiquidity_SettlesResolvableSetInOneTransaction(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T_BIG", BankFX, 1_500_000_00, 1)
	f.matched("T_MED", BankFX, 1_000_000_00, 1)
	f.matched("T_SMALL", BankFX, 500_000_00, 1)
	pv, err := f.previewLiquidity("B_ONE", "T_SMALL", "T_MED", "T_BIG")
	if err != nil {
		t.Fatal(err)
	}

	medKey := ""
	for k := range f.l.committed {
		if strings.Contains(k, "T_MED") && strings.Contains(k, keyTrade) {
			medKey = k
		}
	}
	medBefore := string(f.l.committed[medKey])

	res := f.liquiditySettle(mspIN, "B_ONE", "T_SMALL", "T_MED", "T_BIG")
	f.mustOK(res)
	if !res.Committed {
		t.Fatal("not committed")
	}
	if _, wrote := res.Writes[medKey]; wrote {
		t.Fatal("the dropped trade T_MED was written")
	}
	if string(f.l.committed[medKey]) != medBefore {
		t.Fatal("the dropped trade T_MED changed")
	}

	b := f.batchRecord("B_ONE")
	if !reflect.DeepEqual(b.TradeIDs, pv.Plan.SettledTradeIDs) || !reflect.DeepEqual(b.Net, pv.Plan.NetPlan.Net) {
		t.Fatalf("settled batch %v/%v differs from preview %v/%v", b.TradeIDs, b.Net, pv.Plan.SettledTradeIDs, pv.Plan.NetPlan.Net)
	}
	if b.Liquidity == nil || !reflect.DeepEqual(b.Liquidity.DroppedTradeIDs, []string{"T_MED"}) || len(b.Liquidity.Removals) != 1 {
		t.Fatalf("batch liquidity detail %+v", b.Liquidity)
	}
	if !reflect.DeepEqual(b.BalancesAfter, f.balances().Balances) {
		t.Fatalf("batch BalancesAfter %v != ledger %v", b.BalancesAfter, f.balances().Balances)
	}

	f.mustReject(ErrReplay, func() txResult { return f.liquiditySettle(mspIN, "B_ONE", "T_MED") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.liquiditySettle(mspIN, "B_TWO", "T_BIG") })
	f.mustInvariantHolds()
}

// The request's trade-ID order never changes the plan.
func TestLiquidity_PreviewIndependentOfRequestOrder(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T_BIG", BankFX, 1_500_000_00, 1)
	f.matched("T_MED", BankFX, 1_000_000_00, 1)
	f.matched("T_SMALL", BankFX, 500_000_00, 1)
	f.matched4("U1", BankUS, BankSG, 400_000_00, 1)
	ref := f.previewLiquidityRaw("B_ORD", "T_BIG", "T_MED", "T_SMALL", "U1")
	for _, ids := range [][]string{
		{"U1", "T_SMALL", "T_MED", "T_BIG"},
		{"T_MED", "U1", "T_BIG", "T_SMALL"},
		{"T_SMALL", "T_BIG", "U1", "T_MED"},
	} {
		for i := 0; i < 20; i++ {
			if got := f.previewLiquidityRaw("B_ORD", ids...); got != ref {
				t.Fatalf("order %v changed the plan:\n%s\n%s", ids, ref, got)
			}
		}
	}
}

// The largest shortfall BY VALUE is resolved first. BankIN's INR shortfall
// (3,900 paise) is the first one found and the larger raw number, but at the
// batch's own rate (150 USD : 12,000 INR) it is worth 48.75 USD, less than
// BankFX's 50 USD shortfall. Resolving BankFX first removes P, which then
// leaves BankIN short and ends in gridlock. Taking the first shortfall, or
// comparing raw minor units, would remove Q instead and settle P.
func TestResolver_LargestShortfallByValueResolvedFirst(t *testing.T) {
	trades := []*Trade{
		tr("P", BankFX, BankIN, 50, 3_000),
		tr("Q", BankFX, BankIN, 100, 9_000),
	}
	plan, _, err := resolveGridlock("B", trades, bal(BankIN, INR, 8_100, BankFX, USD, 100))
	if CodeOf(err) != ErrGridlock {
		t.Fatalf("want ERR_GRIDLOCK, got %v", err)
	}
	if got := removalIDs(plan); !reflect.DeepEqual(got, []string{"P", "Q"}) {
		t.Fatalf("removal order %v, want [P Q]", got)
	}
	if r := plan.Removals[0]; r.Bank != BankFX || r.Currency != USD || r.Shortfall != 50 {
		t.Fatalf("first removal must address BankFX's 50 USD shortfall: %+v", r)
	}
}

// Shortfalls of exactly equal value in different currencies tie, and the
// fixed bank order decides.
func TestResolver_CrossCurrencyValueTie_FixedBankOrder(t *testing.T) {
	trades := []*Trade{
		tr("A", BankFX, BankIN, 10, 830), // batch rate 10 USD : 830 INR
	}
	// BankIN is short 830 INR (worth 10 USD); BankFX is short 10 USD.
	// Equal value: BANKIN precedes BANKFX, so the INR shortfall is recorded.
	plan, _, err := resolveGridlock("B", trades, bal())
	if CodeOf(err) != ErrGridlock {
		t.Fatalf("want ERR_GRIDLOCK, got %v", err)
	}
	if r := plan.Removals[0]; r.Bank != BankIN || r.Currency != INR || r.Shortfall != 830 {
		t.Fatalf("tie must go to BankIN (earlier in allBanks): %+v", r)
	}
}

// The value-conservation invariant also guards LiquiditySettle: with value
// created outside the chaincode, the batch is refused and nothing is written.
func TestInvariant_LiquiditySettleRefusesWhenValueCreated(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	k, _ := shimKey(keyBalance, BankFX, USD)
	var v int64
	_ = json.Unmarshal(f.l.committed[k], &v)
	f.l.committed[k] = []byte(mustJSON(t, v+1))

	err := f.mustReject(ErrInvariantViolation, func() txResult { return f.liquiditySettle(mspIN, "B_INV", "T1") })
	if !strings.Contains(err.Error(), "value conservation breached for USD") {
		t.Fatalf("unexpected message: %v", err)
	}
}
