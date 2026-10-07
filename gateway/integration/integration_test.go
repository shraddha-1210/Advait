//go:build integration

// Integration tests against the RUNNING Drunix network and gateway.
//
//	cd gateway && go test -tags integration ./integration/ -v -count=1
//
// Requires: network up, pvp chaincode deployed + initialised, gateway on
// GATEWAY_URL (default http://localhost:8080). Tests read the real current
// balances and size their trades from them, so they can be re-run.
//
// Every critical fact is cross-checked by querying BOTH banks' peers directly
// over separate gateway connections, not only through the HTTP API.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/paths"
)

var (
	baseURL = envOr("GATEWAY_URL", "http://localhost:8080")
	orgsDir = paths.OrgsDir() // DRUNIX_ORGS, else $DRUNIX_HOME/..., else /root/drunix/...
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// ---------------------------------------------------------------------------
// HTTP helpers

func call(t *testing.T, method, path string, body any, out any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, raw)
		}
	}
}

type outcome struct {
	OK          bool   `json:"ok"`
	TxID        string `json:"txId"`
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	BlockNumber uint64 `json:"blockNumber"`
}

type snapshot struct {
	Balances       map[string]map[string]int64 `json:"balances"`
	InvariantHolds bool                        `json:"invariantHolds"`
}

type writeResult struct {
	Outcome       outcome  `json:"outcome"`
	Before        snapshot `json:"before"`
	After         snapshot `json:"after"`
	BalancesMoved bool     `json:"balancesMoved"`
}

type attackReport struct {
	Name         string       `json:"name"`
	TradeID      string       `json:"tradeId"`
	Threat       string       `json:"threat"`
	Expect       string       `json:"expect"`
	Setup        []outcome    `json:"setup"`
	Result       *writeResult `json:"result"`
	Refused      bool         `json:"refused"`
	ExpectedCode bool         `json:"expectedCode"`
	Note         string       `json:"note"`
}

func latestSeq(t *testing.T) int64 {
	var st struct {
		Rates struct {
			Head int64 `json:"head"`
		} `json:"rates"`
	}
	call(t, "GET", "/api/state", nil, &st)
	return st.Rates.Head
}

func quote(t *testing.T, usd, seq int64) int64 {
	var q struct {
		INRAmount int64 `json:"inrAmount"`
	}
	call(t, "GET", fmt.Sprintf("/api/quote?usd=%d&seq=%d", usd, seq), nil, &q)
	return q.INRAmount
}

func tradeID(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano()%1e12, 36)
}

// instructBoth has both banks instruct via the HTTP API. Returns the INR leg.
func instructBoth(t *testing.T, id, usdDeliverer string, usd int64) int64 {
	t.Helper()
	seq := latestSeq(t)
	inr := quote(t, usd, seq)
	for _, as := range []string{"BANKIN", "BANKFX"} {
		var res writeResult
		call(t, "POST", "/api/instructions", map[string]any{
			"as": as, "tradeId": id, "usdDeliverer": usdDeliverer,
			"usdAmount": strconv.FormatInt(usd, 10), "inrAmount": strconv.FormatInt(inr, 10), "rateSeq": seq,
		}, &res)
		if !res.Outcome.OK {
			t.Fatalf("%s instruction for %s failed: %s %s", as, id, res.Outcome.Code, res.Outcome.Message)
		}
		if res.BalancesMoved {
			t.Fatalf("an instruction must not move balances")
		}
	}
	return inr
}

// ---------------------------------------------------------------------------
// Direct peer checks (independent of the HTTP API)

func direct(t *testing.T) *ledger.Client {
	t.Helper()
	c, err := ledger.Connect(ledger.DefaultConfig(orgsDir))
	if err != nil {
		t.Fatalf("direct connect: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

type peerView struct {
	Balances     map[string]map[string]int64 `json:"balances"`
	Conservation []struct {
		Currency string `json:"currency"`
		Supply   int64  `json:"supply"`
		Sum      int64  `json:"sum"`
		Holds    bool   `json:"holds"`
	} `json:"conservation"`
}

// balancesOn reads balances from ONE bank's peer.
func balancesOn(t *testing.T, c *ledger.Client, p ledger.Party) peerView {
	t.Helper()
	raw, err := c.Evaluate(p, "GetBalances")
	if err != nil {
		t.Fatalf("GetBalances on %s peer: %v", p, err)
	}
	var v peerView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// assertLedgerSane checks, on BOTH banks' peers independently: the two peers
// agree, value is conserved, and no balance is negative.
func assertLedgerSane(t *testing.T, c *ledger.Client) peerView {
	t.Helper()
	in, fx := balancesOn(t, c, ledger.BankIN), balancesOn(t, c, ledger.BankFX)
	for _, bank := range ledgerBanks {
		for _, ccy := range []string{"INR", "USD"} {
			if in.Balances[bank][ccy] != fx.Balances[bank][ccy] {
				t.Fatalf("peers disagree on %s/%s: BankIN peer %d, BankFX peer %d",
					bank, ccy, in.Balances[bank][ccy], fx.Balances[bank][ccy])
			}
			if in.Balances[bank][ccy] < 0 {
				t.Fatalf("NEGATIVE BALANCE %s/%s = %d", bank, ccy, in.Balances[bank][ccy])
			}
		}
	}
	for _, cv := range in.Conservation {
		if !cv.Holds || cv.Sum != cv.Supply {
			t.Fatalf("value conservation broken for %s: sum %d supply %d", cv.Currency, cv.Sum, cv.Supply)
		}
	}
	return in
}

func tradeStatusOn(t *testing.T, c *ledger.Client, p ledger.Party, id string) (status, settledTx string) {
	t.Helper()
	raw, err := c.Evaluate(p, "GetTrade", id)
	if err != nil {
		t.Fatalf("GetTrade %s on %s peer: %v", id, p, err)
	}
	var v struct {
		Trade struct {
			Status    string `json:"status"`
			SettledTx string `json:"settledTx"`
		} `json:"trade"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Trade.Status, v.Trade.SettledTx
}

// ledgerBanks is every bank account on the ledger. BANKUS and BANKSG are
// simulated ledger-level participants within the existing two-org network;
// on a ledger initialised with two banks they read as zero on both peers.
var ledgerBanks = []string{"BANKIN", "BANKFX", "BANKUS", "BANKSG"}

func equalBal(a, b map[string]map[string]int64) bool {
	for _, bank := range ledgerBanks {
		for _, ccy := range []string{"INR", "USD"} {
			if a[bank][ccy] != b[bank][ccy] {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Tests

func TestGatewayHealthy(t *testing.T) {
	var h struct {
		OK bool `json:"ok"`
	}
	call(t, "GET", "/api/health", nil, &h)
	if !h.OK {
		t.Fatal("gateway reports the ledger is not healthy")
	}
	assertLedgerSane(t, direct(t))
}

// A real settlement through the gateway lands on the ledger (both peers),
// moves exactly the trade amounts, and appears in the audit trail.
func TestSettlementLandsOnLedgerAndAuditTrail(t *testing.T) {
	c := direct(t)
	start := assertLedgerSane(t, c)
	const usd = int64(2_500_00) // 2,500.00 USD
	id := tradeID("IT-HAPPY")
	inr := instructBoth(t, id, "BANKFX", usd)

	var res writeResult
	call(t, "POST", "/api/trades/"+id+"/settle", map[string]string{"as": "BANKIN"}, &res)
	if !res.Outcome.OK {
		t.Fatalf("settlement failed: %s %s", res.Outcome.Code, res.Outcome.Message)
	}
	t.Logf("settled %s in tx %s, block %d", id, res.Outcome.TxID, res.Outcome.BlockNumber)

	// Exact movements, checked on the independent peer reads.
	end := assertLedgerSane(t, c)
	want := map[string]map[string]int64{
		"BANKIN": {"INR": start.Balances["BANKIN"]["INR"] - inr, "USD": start.Balances["BANKIN"]["USD"] + usd},
		"BANKFX": {"INR": start.Balances["BANKFX"]["INR"] + inr, "USD": start.Balances["BANKFX"]["USD"] - usd},
	}
	if !equalBal(end.Balances, want) {
		t.Fatalf("balances after settlement:\n got  %v\n want %v", end.Balances, want)
	}

	// The trade is SETTLED by this tx on BOTH banks' peers.
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		if st, tx := tradeStatusOn(t, c, p, id); st != "SETTLED" || tx != res.Outcome.TxID {
			t.Fatalf("%s peer: trade %s status %s settledTx %s, want SETTLED by %s", p, id, st, tx, res.Outcome.TxID)
		}
	}

	// It is in the audit trail with the same tx id.
	var audit struct {
		Log []struct {
			Type string `json:"type"`
			TxID string `json:"txId"`
			Ref  string `json:"ref"`
		} `json:"log"`
	}
	call(t, "GET", "/api/audit", nil, &audit)
	found := false
	for _, e := range audit.Log {
		if e.Type == "SETTLED" && e.Ref == id && e.TxID == res.Outcome.TxID {
			found = true
		}
	}
	if !found {
		t.Fatalf("SETTLED entry for %s (tx %s) not in the audit log", id, res.Outcome.TxID)
	}
}

// The failure path really rolls back: nothing changes on either peer.
func TestUnderfundedRollsBackEverywhere(t *testing.T) {
	c := direct(t)
	var rep attackReport
	call(t, "POST", "/api/attacks/underfunded", nil, &rep)
	before := assertLedgerSane(t, c)
	if !rep.Refused || rep.Result.Outcome.Code != "ERR_INSUFFICIENT_FUNDS" {
		t.Fatalf("underfunded settlement was not refused as expected: %+v", rep.Result.Outcome)
	}
	if rep.Result.BalancesMoved || !equalBal(rep.Result.Before.Balances, rep.Result.After.Balances) {
		t.Fatalf("balances moved on a refused settlement")
	}
	if !equalBal(before.Balances, rep.Result.After.Balances) {
		t.Fatalf("peer balances differ from the gateway's after-snapshot")
	}
	// The trade is fully matched and still unsettled on both peers: no half-state.
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		if s, _ := tradeStatusOn(t, c, p, rep.TradeID); s != "MATCHED" {
			t.Fatalf("%s peer: underfunded trade status %s, want MATCHED (half-settled?)", p, s)
		}
	}
}

// One org's endorsement is not enough: the network invalidates the
// transaction at commit. The same trade then settles with both endorsements.
func TestSingleEndorsementIsInvalidatedByNetwork(t *testing.T) {
	c := direct(t)
	var rep attackReport
	call(t, "POST", "/api/attacks/unilateral-endorsement", nil, &rep)
	o := rep.Result.Outcome
	if o.OK || o.Stage != "commit" || o.Code != "ENDORSEMENT_POLICY_FAILURE" {
		t.Fatalf("single-org settlement: want commit-stage ENDORSEMENT_POLICY_FAILURE, got ok=%v stage=%s code=%s (%s)",
			o.OK, o.Stage, o.Code, o.Message)
	}
	if o.BlockNumber == 0 {
		t.Fatalf("expected the invalid tx to be recorded in a block")
	}
	if rep.Result.BalancesMoved {
		t.Fatalf("balances moved despite ENDORSEMENT_POLICY_FAILURE")
	}
	assertLedgerSane(t, c)
	t.Logf("tx %s ordered into block %d and invalidated: %s", o.TxID, o.BlockNumber, o.Code)

	// Settle the same trade properly: the ONLY difference is the endorsement.
	id := rep.TradeID
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		if s, _ := tradeStatusOn(t, c, p, id); s != "MATCHED" {
			t.Fatalf("%s peer: trade %s status %s after the invalidated tx, want MATCHED", p, id, s)
		}
	}
	var res writeResult
	call(t, "POST", "/api/trades/"+id+"/settle", map[string]string{"as": "BANKFX"}, &res)
	if !res.Outcome.OK {
		t.Fatalf("the same trade with both endorsements should settle: %s %s", res.Outcome.Code, res.Outcome.Message)
	}
	assertLedgerSane(t, c)
}

// GET /api/trades/{id} reads one trade directly. It must find a trade created
// a moment ago and return 404 for an unknown ID.
func TestGetTradeByID(t *testing.T) {
	id := tradeID("IT-GET")
	inr := instructBoth(t, id, "BANKFX", 123_00)
	var v struct {
		Trade struct {
			TradeID      string   `json:"tradeId"`
			Status       string   `json:"status"`
			USDAmount    int64    `json:"usdAmount"`
			INRAmount    int64    `json:"inrAmount"`
			InstructedBy []string `json:"instructedBy"`
		} `json:"trade"`
		Instructions map[string]struct {
			SubmitterMSP string `json:"submitterMsp"`
			TxID         string `json:"txId"`
		} `json:"instructions"`
	}
	call(t, "GET", "/api/trades/"+id, nil, &v)
	if v.Trade.TradeID != id || v.Trade.Status != "MATCHED" || v.Trade.USDAmount != 123_00 || v.Trade.INRAmount != inr {
		t.Fatalf("GET /api/trades/%s returned %+v", id, v.Trade)
	}
	if len(v.Instructions) != 2 || v.Instructions["BANKIN"].TxID == "" || v.Instructions["BANKFX"].TxID == "" {
		t.Fatalf("want both banks' stored instructions, got %+v", v.Instructions)
	}

	resp, err := http.Get(baseURL + "/api/trades/NO-SUCH-" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown trade: want 404, got %d", resp.StatusCode)
	}
}

// Every threat-matrix row is refused with its own code, moves nothing, and
// leaves value conserved on both peers.
func TestThreatMatrixOnRealNetwork(t *testing.T) {
	c := direct(t)
	for _, name := range []string{
		"double-settle", "replay-instruction", "unilateral-instruction", "forged-instruction",
		"mismatched-instruction", "bank-publishes-rate", "unsigned-rate", "tampered-rate", "fake-oracle", "stale-rate",
		"out-of-band-rate", "negative-amount", "overflow-amount", "auditor-writes", "reinit",
	} {
		t.Run(name, func(t *testing.T) {
			var rep attackReport
			call(t, "POST", "/api/attacks/"+name, nil, &rep)
			o := rep.Result.Outcome
			if !rep.Refused || o.OK {
				t.Fatalf("%s was NOT refused", name)
			}
			if !rep.ExpectedCode {
				t.Fatalf("%s refused with %s (%s), expected %s", name, o.Code, o.Message, rep.Expect)
			}
			if rep.Result.BalancesMoved {
				t.Fatalf("%s moved balances", name)
			}
			assertLedgerSane(t, c)
			t.Logf("refused at %s: %s", o.Stage, o.Message)
		})
	}
}

// KNOWLEDGE GAP D9: can two settlements endorsed against the same state both
// commit? Endorse two trades that are each fundable but not together, THEN
// submit both. Correct behaviour: exactly one commits; the other is
// invalidated (MVCC_READ_CONFLICT). A double spend would leave a negative
// balance.
func TestConcurrentDoubleSpendIsPrevented(t *testing.T) {
	c := direct(t)
	start := assertLedgerSane(t, c)
	fxUSD := start.Balances["BANKFX"]["USD"]
	if fxUSD < 200_00 {
		t.Skipf("BankFX USD balance too low for the test: %d", fxUSD)
	}
	each := fxUSD*6/10 + 1 // each trade needs 60% of BankFX's USD: fundable alone, not together
	a, b := tradeID("IT-DS-A"), tradeID("IT-DS-B")
	instructBoth(t, a, "BANKFX", each)
	instructBoth(t, b, "BANKFX", each)

	ea, oa := c.Endorse(ledger.BankIN, "SettleTrade", a)
	eb, ob := c.Endorse(ledger.BankFX, "SettleTrade", b)
	if ea == nil || eb == nil {
		t.Fatalf("both settlements should endorse against the same state: A=%+v B=%+v", oa, ob)
	}
	ra, rb := ea.Submit(), eb.Submit()
	t.Logf("A: ok=%v code=%s block=%d | B: ok=%v code=%s block=%d", ra.OK, ra.Code, ra.BlockNumber, rb.OK, rb.Code, rb.BlockNumber)

	end := assertLedgerSane(t, c) // fails loudly on a negative balance
	if ra.OK && rb.OK {
		t.Fatalf("DOUBLE SPEND: both conflicting settlements committed")
	}
	if !ra.OK && !rb.OK {
		t.Fatalf("neither settlement committed; expected exactly one")
	}
	loser := rb
	if !ra.OK {
		loser = ra
	}
	if loser.Code != "MVCC_READ_CONFLICT" {
		t.Errorf("losing settlement refused with %s, expected MVCC_READ_CONFLICT", loser.Code)
	}
	if got := start.Balances["BANKFX"]["USD"] - end.Balances["BANKFX"]["USD"]; got != each {
		t.Fatalf("BankFX USD fell by %d, want exactly one trade's %d", got, each)
	}
	rebalance(t, each)
}

// rebalance sends USD back to BankFX so repeated runs do not drain it.
func rebalance(t *testing.T, usd int64) {
	id := tradeID("IT-REBAL")
	instructBoth(t, id, "BANKIN", usd)
	var res writeResult
	call(t, "POST", "/api/trades/"+id+"/settle", map[string]string{"as": "BANKIN"}, &res)
	if !res.Outcome.OK {
		t.Logf("rebalance skipped: %s %s", res.Outcome.Code, res.Outcome.Message)
	}
}

// Peer down: with BankFX's endorsing peer stopped, a valid settlement cannot
// complete, nothing moves, and after restart the network recovers.
// Disruptive (stops a container), so opt-in: RUN_DISRUPTIVE=1.
func TestBankOfflineBlocksSettlement(t *testing.T) {
	if os.Getenv("RUN_DISRUPTIVE") != "1" {
		t.Skip("set RUN_DISRUPTIVE=1 to stop/start a peer container")
	}
	c := direct(t)
	var rep attackReport
	call(t, "POST", "/api/attacks/bank-offline", nil, &rep)
	o := rep.Result.Outcome
	// Only a transport failure counts. A chaincode ERR_... or PROPOSAL_ERROR is
	// also reported at stage "endorse" but would mean something else refused.
	if o.OK || o.Stage != "endorse" || (o.Code != "ENDORSER_UNAVAILABLE" && o.Code != "ENDORSE_FAILED") {
		t.Fatalf("settlement with BankFX offline: want endorse-stage ENDORSER_UNAVAILABLE or ENDORSE_FAILED, got ok=%v stage=%s code=%s (%s)",
			o.OK, o.Stage, o.Code, o.Message)
	}
	if !rep.ExpectedCode {
		t.Fatalf("gateway reported expectedCode=false for a %s refusal", o.Code)
	}
	if rep.Result.BalancesMoved {
		t.Fatal("balances moved while BankFX was offline")
	}
	t.Logf("refused: %s | %s | %s", o.Code, o.Message, rep.Note)
	assertLedgerSane(t, c)
	// Recovery: a normal settlement works again.
	id := tradeID("IT-RECOVER")
	instructBoth(t, id, "BANKFX", 100_00)
	var res writeResult
	call(t, "POST", "/api/trades/"+id+"/settle", map[string]string{"as": "BANKIN"}, &res)
	if !res.Outcome.OK {
		t.Fatalf("network did not recover: %s %s", res.Outcome.Code, res.Outcome.Message)
	}
}
