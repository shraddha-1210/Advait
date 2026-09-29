//go:build integration

// The Oracle and the Auditor are their own MSPs on the channel
// (network/add-orgs.sh). These tests use their real identities against the
// live network: the Auditor reads everything and can write nothing; only the
// Oracle org, holding the pinned key, can publish a rate.
package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
)

var oracleKey = envOr("ORACLE_KEY", "../../network/oracle/oracle.key")

type logEntry struct {
	N            int64  `json:"n"`
	Type         string `json:"type"`
	TxID         string `json:"txId"`
	SubmitterMSP string `json:"submitterMsp"`
	Ref          string `json:"ref"`
}

func auditLogAs(t *testing.T, c *ledger.Client, p ledger.Party) []logEntry {
	t.Helper()
	raw, err := c.Evaluate(p, "GetAuditLog")
	if err != nil {
		t.Fatalf("GetAuditLog as %s: %v", p, err)
	}
	var v []logEntry
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func rateHeadAs(t *testing.T, c *ledger.Client, p ledger.Party) int64 {
	t.Helper()
	raw, err := c.Evaluate(p, "GetRates")
	if err != nil {
		t.Fatalf("GetRates as %s: %v", p, err)
	}
	var v struct {
		Head int64 `json:"head"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.Head
}

// The ledger config pins the Auditor and Oracle MSPs that exist on the channel.
func TestRolesPinnedToRealMSPs(t *testing.T) {
	c := direct(t)
	raw, err := c.Evaluate(ledger.Auditor, "GetConfig")
	if err != nil {
		t.Fatalf("GetConfig as Auditor: %v", err)
	}
	var cfg struct {
		AuditorMSPs []string `json:"auditorMsps"`
		OracleMSP   string   `json:"oracleMsp"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.AuditorMSPs) != 1 || cfg.AuditorMSPs[0] != c.MSPID(ledger.Auditor) {
		t.Fatalf("auditorMsps = %v, want [%s]", cfg.AuditorMSPs, c.MSPID(ledger.Auditor))
	}
	if cfg.OracleMSP != c.MSPID(ledger.Oracle) {
		t.Fatalf("oracleMsp = %q, want %q", cfg.OracleMSP, c.MSPID(ledger.Oracle))
	}
}

// The regulator's read path: the gateway's audit endpoint queries with the
// Auditor's own identity, and what the Auditor sees matches both banks' peers.
func TestAuditorReadPath(t *testing.T) {
	c := direct(t)
	start := assertLedgerSane(t, c)

	var a struct {
		QueriedAs string     `json:"queriedAs"`
		Log       []logEntry `json:"log"`
		Rates     struct {
			Head int64 `json:"head"`
		} `json:"rates"`
		Invariant struct {
			Holds bool `json:"holds"`
		} `json:"invariant"`
	}
	call(t, "GET", "/api/audit", nil, &a)
	if !strings.Contains(a.QueriedAs, c.MSPID(ledger.Auditor)) {
		t.Fatalf("audit endpoint queried as %q, want the Auditor's MSP %s", a.QueriedAs, c.MSPID(ledger.Auditor))
	}
	if len(a.Log) == 0 || a.Rates.Head < 1 || !a.Invariant.Holds {
		t.Fatalf("audit view incomplete: %d log entries, rate head %d, invariant holds %v", len(a.Log), a.Rates.Head, a.Invariant.Holds)
	}

	// Direct reads as the Auditor, compared with each bank's own view.
	aud := balancesOn(t, c, ledger.Auditor)
	if !equalBal(aud.Balances, start.Balances) {
		t.Fatalf("Auditor sees %v, banks' peers see %v", aud.Balances, start.Balances)
	}
	for _, cv := range aud.Conservation {
		if !cv.Holds {
			t.Fatalf("Auditor view: conservation broken for %s", cv.Currency)
		}
	}
	log := auditLogAs(t, c, ledger.Auditor)
	for i, e := range log {
		if e.N != int64(i+1) {
			t.Fatalf("audit log not gap-free: entry %d has n=%d", i, e.N)
		}
		if e.Type == "RATE_PUBLISHED" && e.SubmitterMSP != c.MSPID(ledger.Oracle) {
			t.Fatalf("rate seq %s was published by %s, not the oracle org", e.Ref, e.SubmitterMSP)
		}
	}
	for _, fn := range []string{"GetTrades", "CheckInvariant"} {
		if _, err := c.Evaluate(ledger.Auditor, fn); err != nil {
			t.Fatalf("%s as Auditor: %v", fn, err)
		}
	}
}

// The Auditor's identity is refused on every write it tries, and nothing changes.
func TestAuditorCannotWriteOnRealNetwork(t *testing.T) {
	c := direct(t)
	s, err := oracle.Load(oracleKey)
	if err != nil {
		t.Fatalf("oracle key: %v", err)
	}
	before := assertLedgerSane(t, c)
	head := rateHeadAs(t, c, ledger.BankIN)
	logLen := len(auditLogAs(t, c, ledger.BankIN))

	att, _ := json.Marshal(s.Sign(head+1, 83_000_000, time.Now())) // genuinely signed
	seq := latestSeq(t)
	inr := quote(t, 100_00, seq)
	instr, _ := json.Marshal(map[string]any{"tradeId": tradeID("AUD-W"), "asBank": "BANKIN", "usdDeliverer": "BANKFX",
		"usdAmount": "10000", "inrAmount": strconv.FormatInt(inr, 10), "rateSeq": seq})
	for fn, args := range map[string][]string{
		"PublishRate":       {string(att)},
		"SubmitInstruction": {string(instr)},
	} {
		out := c.Submit(ledger.Auditor, fn, args, ledger.SubmitOptions{})
		if out.OK || out.Stage != "endorse" || out.Code != "ERR_UNAUTHORIZED" {
			t.Fatalf("Auditor %s: want refusal ERR_UNAUTHORIZED at endorse, got ok=%v %s %s: %s", fn, out.OK, out.Stage, out.Code, out.Message)
		}
		t.Logf("Auditor %s refused: %s", fn, out.Message)
	}
	after := assertLedgerSane(t, c)
	if !equalBal(before.Balances, after.Balances) {
		t.Fatalf("balances changed after refused Auditor writes")
	}
	if h := rateHeadAs(t, c, ledger.BankIN); h != head {
		t.Fatalf("rate head moved from %d to %d", head, h)
	}
	if n := len(auditLogAs(t, c, ledger.BankIN)); n != logLen {
		t.Fatalf("audit log grew from %d to %d entries", logLen, n)
	}
}

// Only the Oracle org publishes: the same genuinely signed rate is refused from
// a bank and accepted from OracleMSP, on both banks' peers.
func TestOnlyOracleOrgPublishesOnRealNetwork(t *testing.T) {
	c := direct(t)
	s, err := oracle.Load(oracleKey)
	if err != nil {
		t.Fatalf("oracle key: %v", err)
	}
	head := rateHeadAs(t, c, ledger.BankIN)
	att, _ := json.Marshal(s.Sign(head+1, 83_260_000, time.Now()))

	out := c.Submit(ledger.BankFX, "PublishRate", []string{string(att)}, ledger.SubmitOptions{})
	if out.OK || out.Code != "ERR_UNAUTHORIZED" {
		t.Fatalf("BankFX publishing a signed rate: want ERR_UNAUTHORIZED, got ok=%v %s %s", out.OK, out.Code, out.Message)
	}
	out = c.Submit(ledger.Oracle, "PublishRate", []string{string(att)}, ledger.SubmitOptions{})
	if !out.OK {
		t.Fatalf("Oracle org publishing its signed rate failed: %s %s %s", out.Stage, out.Code, out.Message)
	}
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		if h := rateHeadAs(t, c, p); h != head+1 {
			t.Fatalf("rate head on %s peer = %d, want %d", p, h, head+1)
		}
	}
	log := auditLogAs(t, c, ledger.Auditor)
	last := log[len(log)-1]
	if last.Type != "RATE_PUBLISHED" || last.TxID != out.TxID || last.SubmitterMSP != c.MSPID(ledger.Oracle) {
		t.Fatalf("last audit entry %+v, want RATE_PUBLISHED tx %s by %s", last, out.TxID, c.MSPID(ledger.Oracle))
	}
	assertLedgerSane(t, c)
}
