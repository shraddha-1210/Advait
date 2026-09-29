package contract

import (
	"strings"
	"testing"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// The Auditor is a read-only org: every function that writes refuses it,
// with no write attempted, even when the call would otherwise be valid.
func TestRoles_AuditorCannotWrite(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.matched("T2", BankIN, 1_000_00, 1)
	calls := map[string]func() txResult{
		"PublishRate (genuinely signed)": func() txResult { return f.publish(mspAuditor, signed(realOracle, 2, 83_300_000)) },
		"SubmitInstruction": func() txResult {
			return f.instruct(mspAuditor, f.trade("T3", BankIN, BankFX, 1_000_00, 1))
		},
		"SettleTrade": func() txResult { return f.settle(mspAuditor, "T1") },
		"NetSettle":   func() txResult { return f.netSettle(mspAuditor, "B1", "T1", "T2") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			f.t = t
			f.mustReject(ErrUnauthorized, call)
			f.mustInvariantHolds()
		})
	}
	f.t = t
	if tr := f.tradeRecord("T1"); tr.Status != StatusMatched {
		t.Fatalf("T1 status %s after auditor attempts, want %s", tr.Status, StatusMatched)
	}

	// On a fresh ledger the auditor cannot initialise it either.
	g := &fixture{t: t, l: newFakeLedger(), cc: &PvPContract{}}
	raw := mustJSON(t, initRequest())
	g.mustReject(ErrUnauthorized, func() txResult {
		return g.l.invoke(mspAuditor, func(ctx contractapi.TransactionContextInterface) error { return g.cc.InitLedger(ctx, raw) })
	})
}

// The Auditor can read everything a regulator needs.
func TestRoles_AuditorCanReadEverything(t *testing.T) {
	f := newFixtureWithRate(t)
	f.matched("T1", BankFX, 1_000_00, 1)
	f.mustOK(f.settle(mspIN, "T1"))
	f.matched("T2", BankFX, 2_000_00, 1)
	f.matched("T3", BankIN, 1_500_00, 1)
	f.mustOK(f.netSettle(mspFX, "B1", "T2", "T3"))

	reads := map[string]func(contractapi.TransactionContextInterface) (string, error){
		"GetConfig":      func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetConfig(ctx) },
		"GetBalances":    func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetBalances(ctx) },
		"CheckInvariant": func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.CheckInvariant(ctx) },
		"GetRates":       func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetRates(ctx) },
		"GetTrades":      func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetTrades(ctx) },
		"GetTrade":       func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetTrade(ctx, "T1") },
		"GetAuditLog":    func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetAuditLog(ctx) },
		"GetBatch":       func(ctx contractapi.TransactionContextInterface) (string, error) { return f.cc.GetBatch(ctx, "B1") },
		"QuoteINR": func(ctx contractapi.TransactionContextInterface) (string, error) {
			return f.cc.QuoteINR(ctx, "100", "1")
		},
	}
	for name, read := range reads {
		out, err := f.l.query(t, mspAuditor, read)
		if err != nil {
			t.Fatalf("auditor %s: %v", name, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("auditor %s returned nothing", name)
		}
	}
	// The audit log the auditor reads names who did what.
	types := map[string]string{}
	for _, e := range f.auditLog() {
		types[e.Type] = e.SubmitterMSP
	}
	for typ, msp := range map[string]string{"INIT": mspIN, "RATE_PUBLISHED": mspOracle, "SETTLED": mspIN, "NET_SETTLED": mspFX} {
		if types[typ] != msp {
			t.Fatalf("audit log %s submitted by %q, want %q (log types: %v)", typ, types[typ], msp, types)
		}
	}
	f.mustInvariantHolds()
}

// With an oracle MSP pinned, a correctly signed rate is still refused unless
// the oracle org itself submits it: publishing needs the org identity AND the
// pinned signing key (the key alone is covered by the attestation tests).
func TestRoles_OnlyOracleOrgPublishes(t *testing.T) {
	f := newFixture(t)
	for _, msp := range []string{mspIN, mspFX, mspRogue} {
		err := f.mustReject(ErrUnauthorized, func() txResult { return f.publish(msp, signed(realOracle, 1, 83_250_000)) })
		if !strings.Contains(err.Error(), mspOracle) {
			t.Fatalf("message should name the oracle org: %v", err)
		}
	}
	f.mustOK(f.publish(mspOracle, signed(realOracle, 1, 83_250_000)))
	f.mustInvariantHolds()
}

// Without an oracle MSP pinned, any non-auditor member may relay a signed
// rate (the signature is what counts), but the auditor is still refused.
func TestRoles_NoOracleMSP_AnyMemberRelays(t *testing.T) {
	f := &fixture{t: t, l: newFakeLedger(), cc: &PvPContract{}}
	req := initRequest()
	req.OracleMSP = ""
	raw := mustJSON(t, req)
	f.mustOK(f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, raw) }))
	f.mustReject(ErrUnauthorized, func() txResult { return f.publish(mspAuditor, signed(realOracle, 1, 83_250_000)) })
	f.mustOK(f.publish(mspFX, signed(realOracle, 1, 83_250_000)))
	f.mustReject(ErrAttestationBadSignature, func() txResult { return f.publish(mspFX, signed(fakeOracle, 2, 83_250_000)) })
}

func TestInit_RoleMSPsMustNotOverlap(t *testing.T) {
	cases := map[string]func(*InitRequest){
		"oracle is a bank":     func(r *InitRequest) { r.OracleMSP = mspFX },
		"oracle is auditor":    func(r *InitRequest) { r.OracleMSP = mspAuditor },
		"auditor is a bank":    func(r *InitRequest) { r.AuditorMSPs = []string{mspIN} },
		"empty auditor MSP ID": func(r *InitRequest) { r.AuditorMSPs = []string{""} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fixture{t: t, l: newFakeLedger(), cc: &PvPContract{}}
			req := initRequest()
			mutate(&req)
			raw := mustJSON(t, req)
			f.mustReject(ErrInvalidInput, func() txResult {
				return f.l.invoke(mspIN, func(ctx contractapi.TransactionContextInterface) error { return f.cc.InitLedger(ctx, raw) })
			})
		})
	}
}
