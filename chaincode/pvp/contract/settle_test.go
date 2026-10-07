package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// The contract must pass contractapi's metadata validation, or the peer
// will refuse to start it.
func TestContractMetadataIsValid(t *testing.T) {
	if _, err := contractapi.NewChaincode(&PvPContract{}); err != nil {
		t.Fatalf("contractapi rejected the contract: %v", err)
	}
}

func TestInitLedgerSetsBalancesAndFixedSupply(t *testing.T) {
	f := newFixture(t)
	b := f.balances()
	want := map[string]map[string]int64{
		BankIN: {INR: openINR_IN, USD: openUSD_IN},
		BankFX: {INR: openINR_FX, USD: openUSD_FX},
		BankUS: {INR: openINR_US, USD: openUSD_US},
		BankSG: {INR: openINR_SG, USD: openUSD_SG},
	}
	for bank, m := range want {
		for ccy, v := range m {
			if got := b.Balances[bank][ccy]; got != v {
				t.Errorf("%s/%s = %d, want %d", bank, ccy, got, v)
			}
		}
	}
	f.mustInvariantHolds()
	if log := f.auditLog(); len(log) != 1 || log[0].Type != "INIT" {
		t.Fatalf("audit log after init = %+v, want one INIT entry", log)
	}
}

// Happy path: both legs move by exactly the trade amounts, in one transaction.
func TestSettleTradeMovesBothLegsAtomically(t *testing.T) {
	f := newFixtureWithRate(t)
	const usd = int64(10_000_00) // 10,000.00 USD = 1,000,000 cents
	inr := f.matched("T1", BankFX, usd, 1)
	// 10,000.00 USD * 83.25 INR/USD = 832,500.00 INR = 83,250,000 paise.
	if inr != 83_250_000 {
		t.Fatalf("quote = %d paise, want 83250000", inr)
	}
	before := f.balances().Balances

	res := f.settle(mspIN, "T1")
	f.mustOK(res)

	// Both legs are in the SAME transaction's write set.
	for _, acct := range [][2]string{{BankIN, INR}, {BankIN, USD}, {BankFX, INR}, {BankFX, USD}} {
		k, _ := shimKey(BAL, acct[0], acct[1])
		if _, ok := res.Writes[k]; !ok {
			t.Errorf("settlement tx %s did not write %s/%s; both legs must be in one tx", res.TxID, acct[0], acct[1])
		}
	}

	after := f.balances().Balances
	check := func(bank, ccy string, delta int64) {
		t.Helper()
		if got, want := after[bank][ccy], before[bank][ccy]+delta; got != want {
			t.Errorf("%s/%s = %d, want %d (before %d, delta %+d)", bank, ccy, got, want, before[bank][ccy], delta)
		}
	}
	check(BankIN, INR, -inr) // BankIN pays INR
	check(BankFX, INR, +inr) // BankFX receives INR
	check(BankFX, USD, -usd) // BankFX pays USD
	check(BankIN, USD, +usd) // BankIN receives USD
	f.mustInvariantHolds()

	tr := f.tradeRecord("T1")
	if tr.Status != StatusSettled || tr.SettledTx != res.TxID || tr.SettledVia != "GROSS" {
		t.Fatalf("trade record = %+v", tr)
	}
	if tr.BalancesBefore[BankIN][INR] != before[BankIN][INR] || tr.BalancesAfter[BankIN][INR] != after[BankIN][INR] {
		t.Fatalf("trade snapshot does not match real balances: %+v", tr)
	}
	if _, ok := res.Events["Settled"]; !ok {
		t.Fatalf("no Settled event emitted")
	}
}

func TestSettleTradeOtherDirection(t *testing.T) {
	f := newFixtureWithRate(t)
	// First give BankIN some USD by settling a trade the usual way round.
	f.matched("T1", BankFX, 50_000_00, 1)
	f.mustOK(f.settle(mspIN, "T1"))
	before := f.balances().Balances

	// Now BankIN delivers USD and BankFX delivers INR.
	const usd = int64(20_000_00)
	inr := f.matched("T2", BankIN, usd, 1)
	f.mustOK(f.settle(mspFX, "T2"))
	after := f.balances().Balances

	if after[BankIN][USD] != before[BankIN][USD]-usd || after[BankFX][USD] != before[BankFX][USD]+usd ||
		after[BankFX][INR] != before[BankFX][INR]-inr || after[BankIN][INR] != before[BankIN][INR]+inr {
		t.Fatalf("reverse-direction settlement moved wrong amounts:\nbefore %v\nafter  %v", before, after)
	}
	f.mustInvariantHolds()
}

// A settlement that cannot pay one leg writes nothing, including the leg
// that could have been paid.
func TestUnderfundedSettlementWritesNothing(t *testing.T) {
	f := newFixtureWithRate(t)
	// BankFX holds 2,000,000 USD. Ask it to deliver 3,000,000 USD.
	// The INR leg (3m * 83.25 = 249.75m INR) IS fundable: BankIN holds 500m.
	// So exactly one leg is short, and the fundable leg must not move either.
	const tooMuch = int64(3_000_000_00)
	inr := f.quote(tooMuch, 1)
	before := f.balances()
	if before.Balances[BankIN][INR] < inr || before.Balances[BankFX][USD] >= tooMuch {
		t.Fatalf("test setup: want INR leg fundable and USD leg short")
	}
	f.matched("SHORT", BankFX, tooMuch, 1)
	before = f.balances()

	err := f.mustReject(ErrInsufficientFunds, func() txResult { return f.settle(mspIN, "SHORT") })
	if !strings.Contains(err.Error(), "BANKFX holds") || !strings.Contains(err.Error(), "Neither leg was paid") {
		t.Fatalf("rejection should name the short account: %v", err)
	}
	if after := f.balances(); !equalBalances(before.Balances, after.Balances) {
		t.Fatalf("balances changed after rejected settlement:\nbefore %v\nafter  %v", before.Balances, after.Balances)
	}
	if st := f.tradeRecord("SHORT").Status; st != StatusMatched {
		t.Fatalf("trade status after rejection = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

func TestUnderfundedReportsEveryShortLeg(t *testing.T) {
	f := newFixtureWithRate(t)
	// BankIN delivering USD: BankIN has 0 USD. BankFX delivering INR: has 0 INR.
	f.matched("BOTHSHORT", BankIN, 100_00, 1)
	err := f.mustReject(ErrInsufficientFunds, func() txResult { return f.settle(mspFX, "BOTHSHORT") })
	if !strings.Contains(err.Error(), "BANKIN holds 0 USD") || !strings.Contains(err.Error(), "BANKFX holds 0 INR") {
		t.Fatalf("both shortfalls should be reported: %v", err)
	}
}

// Rounding rule: INR leg = USD leg * rate, half-up, in integer minor units.
func TestConvertUSDToINR(t *testing.T) {
	cases := []struct{ usd, rate, want int64 }{
		{100, 83_250_000, 8_325},            // 1.00 USD at 83.25 = 83.25 INR
		{1, 83_255_000, 83},                 // 0.01 USD at 83.255 = 0.83255 -> 83 paise
		{1, 83_500_000, 84},                 // 0.835 -> half-up to 84 paise
		{1, 83_499_999, 83},                 // just below half
		{10_000_00, 83_250_000, 83_250_000}, // 10,000.00 USD -> 832,500.00 INR
	}
	for _, c := range cases {
		got, err := ConvertUSDToINR(c.usd, c.rate)
		if err != nil || got != c.want {
			t.Errorf("ConvertUSDToINR(%d, %d) = %d, %v; want %d", c.usd, c.rate, got, err, c.want)
		}
	}
	if _, err := ConvertUSDToINR(MaxAmountMinor, MaxRateMicros); CodeOf(err) != ErrAmountOverflow {
		t.Errorf("huge conversion: got %v, want %s", err, ErrAmountOverflow)
	}
}

func TestQueriesAreReadOnlyAndAuditLogOrdered(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.mustOK(f.settle(mspIN, "T1"))
	var types []string
	for i, e := range f.auditLog() {
		if e.N != int64(i+1) {
			t.Fatalf("audit log entry %d has N=%d", i, e.N)
		}
		types = append(types, e.Type)
	}
	want := "INIT,RATE_PUBLISHED,INSTRUCTED,INSTRUCTED,SETTLED"
	if got := strings.Join(types, ","); got != want {
		t.Fatalf("audit log = %s, want %s", got, want)
	}
	out, err := f.l.query(t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.GetTrades(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	var trades []Trade
	if err := json.Unmarshal([]byte(out), &trades); err != nil || len(trades) != 1 || trades[0].Status != StatusSettled {
		t.Fatalf("GetTrades = %s (%v)", out, err)
	}
}

// Two endorsing peers must produce byte-identical write sets, or the
// transaction fails. Run the same sequence on two independent ledgers and
// compare every committed byte (catches map-order nondeterminism).
func TestDeterministicAcrossPeers(t *testing.T) {
	run := func() map[string]string {
		f := newFixtureWithRate(t)
		f.mustOK(f.publish(mspOracle, signed(realOracle, 2, 83_310_000)))
		f.matched("A", BankFX, 12_345_67, 2)
		f.mustOK(f.settle(mspIN, "A"))
		f.matched("B", BankIN, 1_000_00, 2)
		f.mustOK(f.settle(mspFX, "B"))
		return f.l.snapshotState()
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("state sizes differ: %d vs %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("key %q differs between runs:\n%s\n%s", k, v, b[k])
		}
	}
}

// ---------------------------------------------------------------------------

const BAL = keyBalance

func shimKey(objectType string, attrs ...string) (string, error) {
	return (&txStub{}).CreateCompositeKey(objectType, attrs)
}

func equalBalances(a, b map[string]map[string]int64) bool {
	for _, bank := range allBanks {
		for _, ccy := range allCurrencies {
			if a[bank][ccy] != b[bank][ccy] {
				return false
			}
		}
	}
	return true
}
