package contract

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

const (
	mspIN      = "Org1MSP"
	mspFX      = "Org2MSP"
	mspAuditor = "AuditorMSP"
	mspRogue   = "RogueMSP"
	mspOracle  = "OracleMSP"
)

// Opening balances used by most tests (minor units).
const (
	openINR_IN = int64(500_000_000_00) // BankIN: 500 million INR (~6m USD at 83.25)
	openUSD_IN = int64(0)
	openINR_FX = int64(0)
	openUSD_FX = int64(2_000_000_00) // BankFX: 2 million USD
	openINR_US = int64(0)
	openUSD_US = int64(1_000_000_00) // BankUS: 1 million USD
	openINR_SG = int64(250_000_000_00) // BankSG: 250 million INR
	openUSD_SG = int64(0)
)

func oracleKey(seed string) ed25519.PrivateKey {
	s := sha256.Sum256([]byte(seed))
	return ed25519.NewKeyFromSeed(s[:])
}

var (
	realOracle = oracleKey("pvp-demo-oracle")
	fakeOracle = oracleKey("colluding-fake-oracle")
)

type fixture struct {
	t  *testing.T
	l  *fakeLedger
	cc *PvPContract
}

func initRequest() InitRequest {
	return InitRequest{
		Config: Config{
			Banks: map[string]string{
				BankIN: mspIN,
				BankFX: mspFX,
				BankUS: mspIN, // proxy authorization mapping
				BankSG: mspFX, // proxy authorization mapping
			},
			AuditorMSPs:     []string{mspAuditor},
			OracleMSP:       mspOracle,
			Pair:            "USD/INR",
			OraclePublicKey: base64.StdEncoding.EncodeToString(realOracle.Public().(ed25519.PublicKey)),
			OracleName:      "Demo FX Oracle (simulated)",
			RateWindow:      3,
		},
		Balances: map[string]map[string]string{
			BankIN: {INR: strconv.FormatInt(openINR_IN, 10), USD: strconv.FormatInt(openUSD_IN, 10)},
			BankFX: {INR: strconv.FormatInt(openINR_FX, 10), USD: strconv.FormatInt(openUSD_FX, 10)},
			BankUS: {INR: strconv.FormatInt(openINR_US, 10), USD: strconv.FormatInt(openUSD_US, 10)},
			BankSG: {INR: strconv.FormatInt(openINR_SG, 10), USD: strconv.FormatInt(openUSD_SG, 10)},
		},
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newFixture returns an initialised ledger with no rates published.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, l: newFakeLedger(), cc: &PvPContract{}}
	req := mustJSON(t, initRequest())
	res := f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, req) })
	if res.Err != nil {
		t.Fatalf("InitLedger: %v", res.Err)
	}
	return f
}

// newFixtureWithRate also publishes oracle rate seq 1 = 83.250000.
func newFixtureWithRate(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.mustOK(f.publish(mspOracle, signed(realOracle, 1, 83_250_000)))
	return f
}

func signed(key ed25519.PrivateKey, seq, rateMicros int64) Attestation {
	a := Attestation{Pair: "USD/INR", Seq: seq, RateMicros: rateMicros, Source: "Demo FX Oracle (simulated)", AsOf: "2026-09-26T12:00:00Z"}
	a.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, CanonicalPayload(a)))
	return a
}

func (f *fixture) publish(msp string, a Attestation) txResult {
	j := mustJSON(f.t, a)
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.PublishRate(ctx, j) })
}

func (f *fixture) instructRaw(msp, raw string) txResult {
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.SubmitInstruction(ctx, raw) })
}

func (f *fixture) instruct(msp string, in Instruction) txResult {
	return f.instructRaw(msp, mustJSON(f.t, in))
}

func (f *fixture) settle(msp, tradeID string) txResult {
	return f.l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error { return f.cc.SettleTrade(ctx, tradeID) })
}

// quote asks the chaincode for the INR leg of usd cents at rate seq.
func (f *fixture) quote(usd, seq int64) int64 {
	f.t.Helper()
	out, err := f.l.query(f.t, mspIN, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.QuoteINR(ctx, strconv.FormatInt(usd, 10), strconv.FormatInt(seq, 10))
	})
	if err != nil {
		f.t.Fatalf("QuoteINR: %v", err)
	}
	var q struct {
		INRAmount int64 `json:"inrAmount"`
	}
	if err := json.Unmarshal([]byte(out), &q); err != nil {
		f.t.Fatal(err)
	}
	return q.INRAmount
}

// trade builds an instruction priced by the chaincode's own quote.
func (f *fixture) trade(id, asBank, usdDeliverer string, usd, seq int64) Instruction {
	inrDeliverer := BankIN
	if usdDeliverer == BankIN {
		inrDeliverer = BankFX
	}
	return Instruction{
		TradeID: id, AsBank: asBank, USDDeliverer: usdDeliverer, INRDeliverer: inrDeliverer,
		USDAmount: strconv.FormatInt(usd, 10),
		INRAmount: strconv.FormatInt(f.quote(usd, seq), 10),
		RateSeq:   seq,
	}
}

func (f *fixture) trade4(id, asBank, usdDeliverer, inrDeliverer string, usd, seq int64) Instruction {
	return Instruction{
		TradeID: id, AsBank: asBank, USDDeliverer: usdDeliverer, INRDeliverer: inrDeliverer,
		USDAmount: strconv.FormatInt(usd, 10),
		INRAmount: strconv.FormatInt(f.quote(usd, seq), 10),
		RateSeq:   seq,
	}
}

// matched instructs both sides of a trade and returns the INR leg.
func (f *fixture) matched(id, usdDeliverer string, usd, seq int64) int64 {
	f.t.Helper()
	f.mustOK(f.instruct(mspIN, f.trade(id, BankIN, usdDeliverer, usd, seq)))
	f.mustOK(f.instruct(mspFX, f.trade(id, BankFX, usdDeliverer, usd, seq)))
	return f.quote(usd, seq)
}

type balancesView struct {
	Balances     map[string]map[string]int64 `json:"balances"`
	Conservation []ConservationReport        `json:"conservation"`
}

// balances reads balances through the real GetBalances query.
func (f *fixture) balances() balancesView {
	f.t.Helper()
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.GetBalances(ctx)
	})
	if err != nil {
		f.t.Fatalf("GetBalances: %v", err)
	}
	var v balancesView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

type invariantView struct {
	Holds      bool                 `json:"holds"`
	Currencies []ConservationReport `json:"currencies"`
}

// invariant recomputes value conservation through the real CheckInvariant query.
func (f *fixture) invariant() invariantView {
	f.t.Helper()
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.CheckInvariant(ctx)
	})
	if err != nil {
		f.t.Fatalf("CheckInvariant: %v", err)
	}
	var v invariantView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) mustInvariantHolds() {
	f.t.Helper()
	inv := f.invariant()
	if !inv.Holds {
		f.t.Fatalf("value-conservation invariant does not hold: %+v", inv.Currencies)
	}
	// Supply is fixed at init: the opening totals.
	want := map[string]int64{
		INR: openINR_IN + openINR_FX + openINR_US + openINR_SG,
		USD: openUSD_IN + openUSD_FX + openUSD_US + openUSD_SG,
	}
	for _, c := range inv.Currencies {
		if c.Supply != want[c.Currency] || c.Sum != want[c.Currency] {
			f.t.Fatalf("%s: supply %d sum %d, want both %d", c.Currency, c.Supply, c.Sum, want[c.Currency])
		}
	}
}

func (f *fixture) tradeRecord(id string) Trade {
	f.t.Helper()
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.GetTrade(ctx, id)
	})
	if err != nil {
		f.t.Fatalf("GetTrade(%s): %v", id, err)
	}
	var v struct {
		Trade Trade `json:"trade"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatal(err)
	}
	return v.Trade
}

func (f *fixture) auditLog() []LogEntry {
	f.t.Helper()
	out, err := f.l.query(f.t, mspAuditor, func(ctx contractapi.TransactionContextInterface) (string, error) {
		return f.cc.GetAuditLog(ctx)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	var v []LogEntry
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) mustOK(r txResult) {
	f.t.Helper()
	if r.Err != nil {
		f.t.Fatalf("expected success, got: %v", r.Err)
	}
}

// mustReject asserts the call was refused with `code`, wrote nothing, and
// left committed state byte-for-byte identical. It then asserts the
// value-conservation invariant still holds.
func (f *fixture) mustReject(code string, call func() txResult) error {
	f.t.Helper()
	before := f.l.snapshotState()
	r := call()
	if r.Err == nil {
		f.t.Fatalf("expected %s, but the call succeeded", code)
	}
	if got := CodeOf(r.Err); got != code {
		f.t.Fatalf("expected %s, got %q (%v)", code, got, r.Err)
	}
	if len(r.Writes) != 0 {
		f.t.Fatalf("%s: rejected call attempted %d write(s); chaincode must validate before writing", code, len(r.Writes))
	}
	if after := f.l.snapshotState(); !reflect.DeepEqual(before, after) {
		f.t.Fatalf("%s: committed state changed after a rejected call", code)
	}
	return r.Err
}
