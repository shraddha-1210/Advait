//go:build integration

package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
)

// Liquidity-engine tests against the live network. They need the
// liquidity-engine chaincode deployed as pvp-le (next to pvp, same channel)
// and initialised with all four ledger banks, and the gateway and this test
// both pointed at it:
//
//	CC_NAME=pvp-le bash network/deploy-cc.sh 1.0 1
//	CHAINCODE_NAME=pvp-le /root/bin/pvpctl init network/oracle/oracle.pub
//	CHAINCODE_NAME=pvp-le /root/bin/pvpctl publish network/oracle/oracle.key 83250000
//	CHAINCODE_NAME=pvp-le bash scripts/run-gateway.sh
//	cd gateway && CHAINCODE_NAME=pvp-le go test -tags integration ./integration/ -run 'Liquidity|ScanHeadroom' -v -count=1

func requirePvpLE(t *testing.T) {
	t.Helper()
	if ledger.ChaincodeName() != "pvp-le" {
		t.Skip("liquidity tests run against the pvp-le chaincode; set CHAINCODE_NAME=pvp-le")
	}
}

func custodianOf(bank string) string {
	if bank == "BANKIN" || bank == "BANKUS" {
		return "BANKIN"
	}
	return "BANKFX"
}

// instruct4 has each side of a four-bank trade instructed by the org that
// custodies it, through the HTTP API.
func instruct4(t *testing.T, id, usdDeliverer, inrDeliverer string, usd int64) {
	t.Helper()
	seq := latestSeq(t)
	inr := quote(t, usd, seq)
	for _, side := range []string{usdDeliverer, inrDeliverer} {
		var res writeResult
		call(t, "POST", "/api/instructions", map[string]any{
			"as": custodianOf(side), "asBank": side, "tradeId": id,
			"usdDeliverer": usdDeliverer, "inrDeliverer": inrDeliverer,
			"usdAmount": strconv.FormatInt(usd, 10), "inrAmount": strconv.FormatInt(inr, 10), "rateSeq": seq,
		}, &res)
		if !res.Outcome.OK {
			t.Fatalf("%s instruction for %s failed: %s %s", side, id, res.Outcome.Code, res.Outcome.Message)
		}
	}
}

type liqPreview struct {
	Plan struct {
		SettledTradeIDs []string `json:"settledTradeIds"`
		DroppedTradeIDs []string `json:"droppedTradeIds"`
		Gridlocked      bool     `json:"gridlocked"`
		Cycles          []any    `json:"cycles"`
		NetPlan         *netPlan `json:"netPlan"`
	} `json:"plan"`
	FundsOK bool `json:"fundsOk"`
}

type liqSettleResp struct {
	BatchID string          `json:"batchId"`
	Result  *writeResult    `json:"result"`
	Batch   json.RawMessage `json:"batch"`
}

// A four-bank cycle that no single trade could settle gross settles as one
// transaction with no balance moving; every trade is SETTLED on both peers.
func TestLiquidityFourBankCycleOnRealNetwork(t *testing.T) {
	requirePvpLE(t)
	c := direct(t)
	before := assertLedgerSane(t, c)
	ids := []string{tradeID("IT-LQ-C1"), tradeID("IT-LQ-C2"), tradeID("IT-LQ-C3"), tradeID("IT-LQ-C4")}
	instruct4(t, ids[0], "BANKIN", "BANKFX", 1_000_00)
	instruct4(t, ids[1], "BANKFX", "BANKUS", 1_000_00)
	instruct4(t, ids[2], "BANKUS", "BANKSG", 1_000_00)
	instruct4(t, ids[3], "BANKSG", "BANKIN", 1_000_00)

	var pv liqPreview
	call(t, "POST", "/api/liquidity/preview", map[string]any{"tradeIds": ids}, &pv)
	if !pv.FundsOK || len(pv.Plan.SettledTradeIDs) != 4 || len(pv.Plan.Cycles) != 2 {
		t.Fatalf("preview: %+v", pv)
	}

	var res liqSettleResp
	call(t, "POST", "/api/liquidity/settle", map[string]any{"as": "BANKIN", "tradeIds": ids}, &res)
	if !res.Result.Outcome.OK {
		t.Fatalf("settle failed: %s %s", res.Result.Outcome.Code, res.Result.Outcome.Message)
	}
	if res.Result.BalancesMoved {
		t.Fatal("a fully circular batch must move no balance")
	}
	after := assertLedgerSane(t, c)
	if !equalBal(before.Balances, after.Balances) {
		t.Fatalf("balances moved: %v -> %v", before.Balances, after.Balances)
	}
	for _, id := range ids {
		for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
			if st, tx := tradeStatusOn(t, c, p, id); st != "SETTLED" || tx != res.Result.Outcome.TxID {
				t.Fatalf("%s on %s peer: %s in %s, want SETTLED in %s", id, p, st, tx, res.Result.Outcome.TxID)
			}
		}
	}
}

// A batch BankUS cannot fund is gridlocked: refused with ERR_GRIDLOCK, no
// balance moves on either peer, and the trade stays MATCHED.
func TestLiquidityGridlockOnRealNetwork(t *testing.T) {
	requirePvpLE(t)
	c := direct(t)
	before := assertLedgerSane(t, c)
	id := tradeID("IT-LQ-G")
	instruct4(t, id, "BANKUS", "BANKSG", before.Balances["BANKUS"]["USD"]+1)

	var pv liqPreview
	call(t, "POST", "/api/liquidity/preview", map[string]any{"tradeIds": []string{id}}, &pv)
	if pv.FundsOK || !pv.Plan.Gridlocked || len(pv.Plan.SettledTradeIDs) != 0 {
		t.Fatalf("preview should report gridlock: %+v", pv)
	}
	var res liqSettleResp
	call(t, "POST", "/api/liquidity/settle", map[string]any{"as": "BANKFX", "tradeIds": []string{id}}, &res)
	if res.Result.Outcome.OK || res.Result.Outcome.Code != "ERR_GRIDLOCK" || res.Result.BalancesMoved {
		t.Fatalf("want ERR_GRIDLOCK with nothing moved, got %+v", res.Result.Outcome)
	}
	after := assertLedgerSane(t, c)
	if !equalBal(before.Balances, after.Balances) {
		t.Fatal("balances moved on a gridlocked batch")
	}
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		if st, _ := tradeStatusOn(t, c, p, id); st != "MATCHED" {
			t.Fatalf("%s on %s peer = %s, want MATCHED", id, p, st)
		}
	}
}

// Drunix's SQL state database returns at most 10 rows from an unpaginated
// scan and may repeat a row; the invariant refuses to run once a balance scan
// reaches 10 rows. With 4 banks x 2 currencies there are 8 balance keys, so
// two repeated rows would make every settlement fail closed. This checks the
// live scan repeatedly on both peers: it must count exactly 8 accounts and
// never hit the cap.
func TestBalanceScanHeadroomOnRealNetwork(t *testing.T) {
	requirePvpLE(t)
	c := direct(t)
	for i := 0; i < 25; i++ {
		for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
			raw, err := c.Evaluate(p, "CheckInvariant")
			if err != nil {
				if strings.Contains(err.Error(), "balance scan reached") {
					t.Fatalf("run %d on %s peer: the balance scan hit the 10-row cap (repeated rows): %v", i, p, err)
				}
				t.Fatalf("run %d on %s peer: %v", i, p, err)
			}
			var v struct {
				Holds      bool `json:"holds"`
				Currencies []struct {
					Currency string `json:"currency"`
					Accounts int    `json:"accounts"`
				} `json:"currencies"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			for _, cur := range v.Currencies {
				if cur.Accounts != 4 {
					t.Fatalf("run %d on %s peer: %s summed %d accounts, want 4", i, p, cur.Currency, cur.Accounts)
				}
			}
			if !v.Holds {
				t.Fatalf("run %d on %s peer: invariant does not hold", i, p)
			}
		}
	}
}
