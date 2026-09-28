package contract

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Threat matrix (CLAUDE.md §6). Every row below is a real call into the
// chaincode that must be refused with its own error code. mustReject also
// asserts that the refused call attempted no writes, that committed state is
// byte-for-byte unchanged, and (via mustInvariantHolds) that value is
// conserved after the attack.
//
// NOT covered here, by design: the AND(BankIN, BankFX) endorsement policy.
// Endorsement is evaluated by the committing peers, not by chaincode, so no
// unit test can prove it. It is proven against the real Drunix network in
// the integration suite (single-org endorsement -> transaction invalidated).

func TestThreat_DoubleSettle(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.mustOK(f.settle(mspIN, "T1"))
	f.mustReject(ErrAlreadySettled, func() txResult { return f.settle(mspIN, "T1") })
	f.mustReject(ErrAlreadySettled, func() txResult { return f.settle(mspFX, "T1") }) // either bank
	f.mustInvariantHolds()
}

func TestThreat_ReplayInstructionOfSettledTrade(t *testing.T) {
	f := newFixtureWithRate(t)
	original := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
	f.mustOK(f.instruct(mspIN, original))
	f.mustOK(f.instruct(mspFX, f.trade("T1", BankFX, BankFX, 1_000_00, 1)))
	f.mustOK(f.settle(mspIN, "T1"))
	// Re-submit BankIN's exact original instruction.
	f.mustReject(ErrReplay, func() txResult { return f.instruct(mspIN, original) })
	f.mustInvariantHolds()
}

func TestThreat_UnilateralSettle_OnlyOneInstruction(t *testing.T) {
	f := newFixtureWithRate(t)
	f.mustOK(f.instruct(mspFX, f.trade("T1", BankFX, BankFX, 1_000_00, 1)))
	err := f.mustReject(ErrUnilateral, func() txResult { return f.settle(mspFX, "T1") })
	if !strings.Contains(err.Error(), "only BANKFX") {
		t.Fatalf("message should name who instructed: %v", err)
	}
	f.mustInvariantHolds()
}

func TestThreat_SettleUnknownTrade(t *testing.T) {
	f := newFixtureWithRate(t)
	f.mustReject(ErrTradeNotFound, func() txResult { return f.settle(mspIN, "NOPE") })
}

func TestThreat_NonBankCannotMoveValue(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	for _, msp := range []string{mspAuditor, mspRogue} {
		f.mustReject(ErrUnauthorized, func() txResult { return f.settle(msp, "T1") })
		f.mustReject(ErrUnauthorized, func() txResult {
			return f.instruct(msp, f.trade("T2", BankIN, BankFX, 1_000_00, 1))
		})
	}
	f.mustInvariantHolds()
}

// --- FX oracle manipulation -------------------------------------------------

func TestThreat_UnsignedRate(t *testing.T) {
	f := newFixture(t)
	a := signed(realOracle, 1, 83_250_000)
	a.Signature = ""
	f.mustReject(ErrAttestationUnsigned, func() txResult { return f.publish(mspIN, a) })
}

func TestThreat_PoisonedRate_TamperedAfterSigning(t *testing.T) {
	f := newFixture(t)
	a := signed(realOracle, 1, 83_250_000)
	a.RateMicros = 90_000_000 // attacker edits the rate, keeps the signature
	err := f.mustReject(ErrAttestationBadSignature, func() txResult { return f.publish(mspFX, a) })
	if !strings.Contains(err.Error(), "not signed by the pinned oracle key") {
		t.Fatalf("unexpected message: %v", err)
	}
	// Tampering with the source name is caught too.
	b := signed(realOracle, 1, 83_250_000)
	b.Source = "Totally Real Oracle"
	f.mustReject(ErrAttestationBadSignature, func() txResult { return f.publish(mspFX, b) })
}

func TestThreat_MalformedSignature(t *testing.T) {
	f := newFixture(t)
	a := signed(realOracle, 1, 83_250_000)
	a.Signature = base64.StdEncoding.EncodeToString([]byte("short"))
	f.mustReject(ErrAttestationBadSignature, func() txResult { return f.publish(mspIN, a) })
}

// Malicious bank colludes with a fake oracle: the attestation carries a
// perfectly valid signature, but from a key that was never pinned.
func TestThreat_CollusionWithFakeOracle(t *testing.T) {
	f := newFixture(t)
	f.mustReject(ErrAttestationBadSignature, func() txResult {
		return f.publish(mspFX, signed(fakeOracle, 1, 95_000_000))
	})
}

func TestThreat_StaleRate_OldOrReplayedPublish(t *testing.T) {
	f := newFixtureWithRate(t) // seq 1
	f.mustOK(f.publish(mspIN, signed(realOracle, 2, 83_300_000)))
	f.mustReject(ErrAttestationStale, func() txResult { return f.publish(mspIN, signed(realOracle, 2, 83_300_000)) }) // replay
	f.mustReject(ErrAttestationStale, func() txResult { return f.publish(mspIN, signed(realOracle, 1, 83_250_000)) }) // older
}

func TestThreat_StaleRate_AtInstruction(t *testing.T) {
	f := newFixtureWithRate(t)                       // seq 1; window = 3
	in := f.trade("T1", BankIN, BankFX, 1_000_00, 1) // priced at seq 1
	for seq := int64(2); seq <= 4; seq++ {
		f.mustOK(f.publish(mspIN, signed(realOracle, seq, 83_250_000+seq)))
	}
	// Head is 4; window 3 accepts seq 2,3,4. Seq 1 is stale.
	err := f.mustReject(ErrAttestationStale, func() txResult { return f.instruct(mspIN, in) })
	if !strings.Contains(err.Error(), "seq 1 is stale") {
		t.Fatalf("unexpected message: %v", err)
	}
}

// The rate can go stale between matching and settlement. Settlement must
// re-check it and refuse, moving nothing.
func TestThreat_StaleRate_AtSettlement(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	for seq := int64(2); seq <= 4; seq++ {
		f.mustOK(f.publish(mspIN, signed(realOracle, seq, 83_250_000)))
	}
	f.mustReject(ErrAttestationStale, func() txResult { return f.settle(mspIN, "T1") })
	if st := f.tradeRecord("T1").Status; st != StatusMatched {
		t.Fatalf("status = %s, want MATCHED", st)
	}
	f.mustInvariantHolds()
}

func TestThreat_UnknownRate(t *testing.T) {
	f := newFixtureWithRate(t)
	in := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
	in.RateSeq = 99
	f.mustReject(ErrAttestationUnknown, func() txResult { return f.instruct(mspIN, in) })
}

// Out-of-band rate: both banks could agree to price the trade at a rate the
// oracle never attested. The INR leg must equal the USD leg at the attested rate.
func TestThreat_OutOfBandRate(t *testing.T) {
	f := newFixtureWithRate(t) // attested 83.25
	in := f.trade("T1", BankIN, BankFX, 10_000_00, 1)
	in.INRAmount = "90000000" // 10,000 USD priced at 90.00 instead of 83.25
	err := f.mustReject(ErrRateMismatch, func() txResult { return f.instruct(mspIN, in) })
	if !strings.Contains(err.Error(), "expected 83250000 paise") {
		t.Fatalf("message should state the attested amount: %v", err)
	}
	// Off by a single paisa is still refused.
	in.INRAmount = "83250001"
	f.mustReject(ErrRateMismatch, func() txResult { return f.instruct(mspIN, in) })
}

func TestThreat_AttestationFieldInjection(t *testing.T) {
	f := newFixture(t)
	a := signed(realOracle, 1, 83_250_000)
	a.Source = "Oracle|83250000" // delimiter smuggling
	f.mustReject(ErrInvalidInput, func() txResult { return f.publish(mspIN, a) })
}

// --- Negative / overflow / malformed amounts --------------------------------

func TestThreat_BadAmounts(t *testing.T) {
	f := newFixtureWithRate(t)
	// Each case asserts the code AND the specific reason, so the demo shows
	// exactly which check refused it (and a masked check cannot go unnoticed).
	cases := []struct{ usd, code, reason string }{
		{"-100", ErrInvalidAmount, "is negative"},
		{"0", ErrInvalidAmount, "is zero"},
		{"10.50", ErrInvalidAmount, "not a plain integer"},
		{"1e5", ErrInvalidAmount, "not a plain integer"},
		{"+100", ErrInvalidAmount, "not a plain integer"},
		{" 100", ErrInvalidAmount, "not a plain integer"},
		{"0100", ErrInvalidAmount, "leading zeros"},
		{"", ErrInvalidAmount, "is empty"},
		{"0x10", ErrInvalidAmount, "not a plain integer"},
		{"1000000000000001", ErrAmountOverflow, "exceeds the per-amount cap"}, // cap + 1
		{"9223372036854775808", ErrAmountOverflow, "does not fit in 64 bits"}, // int64 max + 1
		{"99999999999999999999999", ErrAmountOverflow, "does not fit in 64 bits"},
	}
	for _, c := range cases {
		in := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
		in.USDAmount = c.usd
		err := f.mustReject(c.code, func() txResult { return f.instruct(mspIN, in) })
		if !strings.Contains(err.Error(), c.reason) {
			t.Errorf("usdAmount %q: reason should say %q, got: %v", c.usd, c.reason, err)
		}
	}
	for _, c := range []struct{ inr, code, reason string }{
		{"-83250", ErrInvalidAmount, "inrAmount is negative"},
		{"1000000000000001", ErrAmountOverflow, "inrAmount 1000000000000001 exceeds the per-amount cap"},
	} {
		in := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
		in.INRAmount = c.inr
		err := f.mustReject(c.code, func() txResult { return f.instruct(mspIN, in) })
		if !strings.Contains(err.Error(), c.reason) {
			t.Errorf("inrAmount %q: reason should say %q, got: %v", c.inr, c.reason, err)
		}
	}
	f.mustInvariantHolds()
}

// A USD leg inside the cap whose INR leg would exceed it.
func TestThreat_ConversionOverflow(t *testing.T) {
	f := newFixture(t)
	f.mustOK(f.publish(mspIN, signed(realOracle, 1, MaxRateMicros)))
	in := Instruction{TradeID: "BIG", AsBank: BankIN, USDDeliverer: BankFX,
		USDAmount: "1000000000000000", INRAmount: "1000000000000000", RateSeq: 1}
	f.mustReject(ErrAmountOverflow, func() txResult { return f.instruct(mspIN, in) })
}

func TestParseAmountAcceptsOnlyPlainPositiveIntegers(t *testing.T) {
	for _, s := range []string{"1", "10", "1000000000000000"} {
		if _, err := ParseAmount("x", s); err != nil {
			t.Errorf("ParseAmount(%q) = %v", s, err)
		}
	}
	if _, err := addChecked(1<<62, 1<<62); CodeOf(err) != ErrAmountOverflow {
		t.Errorf("addChecked overflow not detected: %v", err)
	}
}

// --- Malicious participant org ----------------------------------------------

func TestThreat_MaliciousOrg_ForgesCounterpartyInstruction(t *testing.T) {
	f := newFixtureWithRate(t)
	// BankFX tries to create BankIN's half of the trade itself.
	forged := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
	err := f.mustReject(ErrForgedInstruction, func() txResult { return f.instruct(mspFX, forged) })
	if !strings.Contains(err.Error(), "Org2MSP") {
		t.Fatalf("message should name the real submitter MSP: %v", err)
	}
}

func TestThreat_MaliciousOrg_SelfMatchByInstructingTwice(t *testing.T) {
	f := newFixtureWithRate(t)
	in := f.trade("T1", BankFX, BankFX, 1_000_00, 1)
	f.mustOK(f.instruct(mspFX, in))
	f.mustReject(ErrDuplicateInstruction, func() txResult { return f.instruct(mspFX, in) })
	f.mustReject(ErrUnilateral, func() txResult { return f.settle(mspFX, "T1") })
}

func TestThreat_MaliciousOrg_MismatchedTerms(t *testing.T) {
	f := newFixtureWithRate(t)
	f.mustOK(f.instruct(mspIN, f.trade("T1", BankIN, BankFX, 10_000_00, 1)))
	// BankFX instructs a smaller USD amount (priced correctly, so it passes the rate check).
	err := f.mustReject(ErrInstructionMismatch, func() txResult {
		return f.instruct(mspFX, f.trade("T1", BankFX, BankFX, 9_000_00, 1))
	})
	if !strings.Contains(err.Error(), "usdAmount 1000000 vs 900000") {
		t.Fatalf("message should list the differing terms: %v", err)
	}
	// Flipping the direction is also a mismatch.
	f.mustReject(ErrInstructionMismatch, func() txResult {
		return f.instruct(mspFX, f.trade("T1", BankFX, BankIN, 10_000_00, 1))
	})
}

// Smuggled fields (e.g. trying to override the rate) are refused, not ignored.
func TestThreat_MaliciousOrg_UnknownFieldSmuggling(t *testing.T) {
	f := newFixtureWithRate(t)
	in := f.trade("T1", BankIN, BankFX, 1_000_00, 1)
	raw := mustJSON(t, in)
	raw = strings.TrimSuffix(raw, "}") + `,"rateMicros":1}`
	f.mustReject(ErrInvalidInput, func() txResult { return f.instructRaw(mspIN, raw) })
	f.mustReject(ErrInvalidInput, func() txResult { return f.instructRaw(mspIN, mustJSON(t, in)+" {}") })
}

// A malicious bank tries to re-run InitLedger to pin its own oracle key and
// re-issue balances.
func TestThreat_MaliciousOrg_ConfigTakeover(t *testing.T) {
	f := newFixtureWithRate(t)
	req := initRequest()
	req.OraclePublicKey = base64.StdEncoding.EncodeToString(fakeOracle.Public().(ed25519.PublicKey))
	req.Balances[BankFX][USD] = "999999999999999"
	raw := mustJSON(t, req)
	f.mustReject(ErrAlreadyInitialized, func() txResult {
		return f.l.invoke(mspFX, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, raw) })
	})
	f.mustInvariantHolds()
}

// Full hostile-participant scenario: BankFX runs every attack it can in
// sequence against an honest BankIN. Afterwards BankIN's balances are exactly
// what they were, every attempt was refused with its own code, and value is
// conserved after every single step.
func TestThreat_MaliciousOrg_FullScenario(t *testing.T) {
	f := newFixtureWithRate(t)
	// One honest trade settles first so there is history to replay.
	f.matched("HONEST", BankFX, 5_000_00, 1)
	f.mustOK(f.settle(mspIN, "HONEST"))
	// BankFX alone instructs a trade that would drain BankIN's INR.
	f.mustOK(f.instruct(mspFX, f.trade("X2", BankFX, BankIN, 1_000_000_00, 1)))
	inBefore := f.balances().Balances[BankIN]

	attacks := []struct {
		name string
		code string
		call func() txResult
	}{
		{"forge BankIN's instruction", ErrForgedInstruction, func() txResult {
			return f.instruct(mspFX, f.trade("X1", BankIN, BankIN, 1_000_000_00, 1))
		}},
		{"settle a trade BankIN never agreed to", ErrUnilateral, func() txResult { return f.settle(mspFX, "X2") }},
		{"re-settle the honest trade", ErrAlreadySettled, func() txResult { return f.settle(mspFX, "HONEST") }},
		{"replay its old instruction", ErrReplay, func() txResult {
			return f.instruct(mspFX, f.trade("HONEST", BankFX, BankFX, 5_000_00, 1))
		}},
		{"publish a rate from a colluding oracle", ErrAttestationBadSignature, func() txResult {
			return f.publish(mspFX, signed(fakeOracle, 2, 50_000_000))
		}},
		{"publish a tampered real rate", ErrAttestationBadSignature, func() txResult {
			a := signed(realOracle, 2, 83_250_000)
			a.RateMicros = 50_000_000
			return f.publish(mspFX, a)
		}},
		{"price a trade off-rate", ErrRateMismatch, func() txResult {
			in := f.trade("X3", BankFX, BankFX, 1_000_00, 1)
			in.INRAmount = "1"
			return f.instruct(mspFX, in)
		}},
		{"negative amount", ErrInvalidAmount, func() txResult {
			in := f.trade("X4", BankFX, BankFX, 1_000_00, 1)
			in.USDAmount = "-1000000"
			return f.instruct(mspFX, in)
		}},
		{"re-initialise the ledger", ErrAlreadyInitialized, func() txResult {
			raw := mustJSON(t, initRequest())
			return f.l.invoke(mspFX, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, raw) })
		}},
	}
	refused := 0
	for _, a := range attacks {
		t.Run(a.name, func(t *testing.T) {
			f.t = t
			f.mustReject(a.code, a.call)
			f.mustInvariantHolds()
			refused++
		})
	}
	f.t = t
	if after := f.balances().Balances[BankIN]; after[INR] != inBefore[INR] || after[USD] != inBefore[USD] {
		t.Fatalf("honest BankIN's balances changed: before %v after %v", inBefore, after)
	}
	if refused != len(attacks) {
		t.Fatalf("%d of %d attacks were refused with their expected code", refused, len(attacks))
	}
	// Refused attacks leave no trace in the audit log; only real commits do.
	for _, e := range f.auditLog() {
		if strings.HasPrefix(e.Ref, "X1") || e.Ref == "X3" || e.Ref == "X4" {
			t.Fatalf("rejected attack appears in the audit log: %+v", e)
		}
	}
}

// --- Value-conservation invariant -------------------------------------------

// Simulate value created outside the rules (a corrupted or malicious earlier
// write): BankFX's USD balance is inflated by one cent directly in state.
// The next value-moving transaction must detect the breach and refuse.
func TestInvariant_DetectsCreatedValueAndRefuses(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	k, _ := shimKey(keyBalance, BankFX, USD)
	var v int64
	_ = json.Unmarshal(f.l.committed[k], &v)
	f.l.committed[k] = []byte(mustJSON(t, v+1))

	inv := f.invariant()
	if inv.Holds {
		t.Fatalf("CheckInvariant should report a breach")
	}
	for _, c := range inv.Currencies {
		if c.Currency == USD && c.Sum != c.Supply+1 {
			t.Fatalf("USD sum %d, supply %d: sum should be supply+1", c.Sum, c.Supply)
		}
	}
	err := f.mustReject(ErrInvariantViolation, func() txResult { return f.settle(mspIN, "T1") })
	if !strings.Contains(err.Error(), "value conservation breached for USD") {
		t.Fatalf("unexpected message: %v", err)
	}
}

// A stray account (e.g. written by modified chaincode on one peer) is found
// because the invariant scans every balance key, not a fixed list.
func TestInvariant_DetectsStrayAccount(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	k, _ := shimKey(keyBalance, "MALLORY", USD)
	f.l.committed[k] = []byte("100000")
	f.mustReject(ErrInvariantViolation, func() txResult { return f.settle(mspIN, "T1") })
}

func TestInvariant_DetectsUnknownCurrency(t *testing.T) {
	f := newFixtureWithRate(t)
	k, _ := shimKey(keyBalance, BankIN, "EUR")
	f.l.committed[k] = []byte("5")
	if f.invariant().Holds {
		t.Fatalf("an unissued currency on the ledger must break the invariant")
	}
}

// The check must refuse a post-state with a negative balance even if the
// total is conserved (e.g. one account -1, another +1).
func TestInvariant_RefusesNegativeBalanceEvenIfTotalConserved(t *testing.T) {
	f := newFixtureWithRate(t)
	all, _, _ := readAllBalances(&txStub{ledger: f.l})
	post := Balances{}
	post.set(BankIN, USD, all.get(BankIN, USD)-1)
	post.set(BankFX, USD, all.get(BankFX, USD)+1)
	if err := assertConservation(&txStub{ledger: f.l}, post); CodeOf(err) != ErrInvariantViolation {
		t.Fatalf("got %v, want %s", err, ErrInvariantViolation)
	}
}

// Property test: a long, seeded sequence of valid and invalid operations.
// After EVERY step the invariant is recomputed from real balances and must hold.
func TestInvariant_HoldsAfterEveryOperation(t *testing.T) {
	f := newFixtureWithRate(t)
	rng := uint64(0x5eed)
	next := func(n uint64) uint64 { // xorshift: deterministic, no math/rand
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return rng % n
	}
	seq := int64(1)
	settled, rejected := 0, 0
	for i := 0; i < 300; i++ {
		id := "P" + strconv.Itoa(i)
		switch next(6) {
		case 0: // new oracle rate
			seq++
			f.mustOK(f.publish(mspIN, signed(realOracle, seq, 80_000_000+int64(next(8_000_000)))))
		case 1: // attack: forged rate
			if r := f.publish(mspFX, signed(fakeOracle, seq+1, 1)); r.Err == nil {
				t.Fatal("forged rate accepted")
			}
			rejected++
		default: // a trade in a random direction and size; may be underfunded
			dir := BankFX
			if next(2) == 0 {
				dir = BankIN
			}
			usd := int64(1 + next(3_000_000_00))
			f.matched(id, dir, usd, seq)
			if r := f.settle(mspIN, id); r.Err == nil {
				settled++
			} else if CodeOf(r.Err) != ErrInsufficientFunds {
				t.Fatalf("step %d: unexpected rejection %v", i, r.Err)
			} else {
				rejected++
			}
		}
		f.mustInvariantHolds()
	}
	if settled == 0 || rejected == 0 {
		t.Fatalf("sequence should exercise both outcomes: settled=%d rejected=%d", settled, rejected)
	}
	t.Logf("300 steps: %d settlements committed, %d attempts rejected, invariant held after every step", settled, rejected)
}

// Griefing (documented limitation, README §11): a hostile bank instructs a
// trade ID first, with terms the honest bank never agreed to (a correctly
// priced 9,000 USD trade instead of 10,000 USD). The honest instruction is
// refused as a mismatch, so the trade is stuck in PENDING_MATCH with only the
// hostile bank's instruction. The attack blocks the trade but cannot move value.
func TestThreat_Griefing_HostileInstructionBlocksLegitimateTrade(t *testing.T) {
	f := newFixtureWithRate(t)
	before := f.balances().Balances

	// Hostile BankFX instructs GRIEVE-1 first, for 9,000 USD.
	f.mustOK(f.instruct(mspFX, f.trade("GRIEVE-1", BankFX, BankFX, 9_000_00, 1)))

	// Honest BankIN instructs the terms it actually agreed: 10,000 USD.
	honest := f.trade("GRIEVE-1", BankIN, BankFX, 10_000_00, 1)
	f.mustReject(ErrInstructionMismatch, func() txResult { return f.instruct(mspIN, honest) })

	tr := f.tradeRecord("GRIEVE-1")
	if tr.Status != StatusPendingMatch {
		t.Fatalf("status = %s, want %s", tr.Status, StatusPendingMatch)
	}
	if !reflect.DeepEqual(tr.InstructedBy, []string{BankFX}) {
		t.Fatalf("instructedBy = %v, want [%s]", tr.InstructedBy, BankFX)
	}

	// Neither bank can settle a trade only one side has instructed.
	f.mustReject(ErrUnilateral, func() txResult { return f.settle(mspIN, "GRIEVE-1") })
	f.mustReject(ErrUnilateral, func() txResult { return f.settle(mspFX, "GRIEVE-1") })

	// mustReject covers the refused calls; this also covers the hostile
	// instruction that succeeded.
	if after := f.balances().Balances; !reflect.DeepEqual(before, after) {
		t.Fatalf("balances moved during griefing: before %v, after %v", before, after)
	}
	f.mustInvariantHolds()
}
