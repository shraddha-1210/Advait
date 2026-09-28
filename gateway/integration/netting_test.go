//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type netLeg struct {
	Currency string `json:"currency"`
	From     string `json:"from"`
	To       string `json:"to"`
	Amount   int64  `json:"amount"`
}

type netPlan struct {
	BatchID  string           `json:"batchId"`
	TradeIDs []string         `json:"tradeIds"`
	Gross    map[string]int64 `json:"gross"`
	Net      []netLeg         `json:"net"`
}

type netPreview struct {
	Plan         netPlan `json:"plan"`
	FundsOK      bool    `json:"fundsOk"`
	FundsMessage string  `json:"fundsMessage"`
}

type netSettleResp struct {
	BatchID string       `json:"batchId"`
	Result  *writeResult `json:"result"`
	Batch   *struct {
		SettledTx string `json:"settledTx"`
		netPlan
	} `json:"batch"`
}

func legFor(p netPlan, ccy string) netLeg {
	for _, l := range p.Net {
		if l.Currency == ccy {
			return l
		}
	}
	return netLeg{}
}

// Bilateral netting on the live network: several matched trades in both
// directions settle as one net movement per currency, in one transaction.
// The balances move by exactly the net, every trade is SETTLED, and a batch
// containing an already-settled trade is refused whole with nothing moving.
func TestNetSettleOnRealNetwork(t *testing.T) {
	c := direct(t)
	a, b, d := tradeID("IT-NET-A"), tradeID("IT-NET-B"), tradeID("IT-NET-C")
	inrA := instructBoth(t, a, "BANKFX", 300_00)
	inrB := instructBoth(t, b, "BANKFX", 200_00)
	inrD := instructBoth(t, d, "BANKIN", 150_00) // the other direction
	ids := []string{a, b, d}

	// Independent arithmetic: BankFX pays USD on A and B, BankIN on D.
	wantUSD := int64(300_00+200_00) - 150_00 // BANKFX -> BANKIN
	wantINR := inrA + inrB - inrD            // BANKIN -> BANKFX

	var pv netPreview
	call(t, "POST", "/api/net-preview", map[string]any{"tradeIds": ids}, &pv)
	if pv.Plan.Gross["USD"] != 650_00 || pv.Plan.Gross["INR"] != inrA+inrB+inrD {
		t.Fatalf("preview gross = %v", pv.Plan.Gross)
	}
	if l := legFor(pv.Plan, "USD"); l.From != "BANKFX" || l.Amount != wantUSD {
		t.Fatalf("preview net USD = %+v, want BANKFX pays %d", l, wantUSD)
	}
	if l := legFor(pv.Plan, "INR"); l.From != "BANKIN" || l.Amount != wantINR {
		t.Fatalf("preview net INR = %+v, want BANKIN pays %d", l, wantINR)
	}
	if !pv.FundsOK {
		t.Fatalf("preview says the net is not funded: %s", pv.FundsMessage)
	}

	var res netSettleResp
	call(t, "POST", "/api/net-settle", map[string]any{"as": "BANKIN", "tradeIds": ids}, &res)
	o := res.Result.Outcome
	if !o.OK {
		t.Fatalf("net settle refused: %s %s", o.Code, o.Message)
	}
	bf, af := res.Result.Before.Balances, res.Result.After.Balances
	for bank, deltas := range map[string]map[string]int64{
		"BANKIN": {"USD": wantUSD, "INR": -wantINR},
		"BANKFX": {"USD": -wantUSD, "INR": wantINR},
	} {
		for ccy, want := range deltas {
			if got := af[bank][ccy] - bf[bank][ccy]; got != want {
				t.Fatalf("%s %s moved %d on the ledger, want exactly the net %d", bank, ccy, got, want)
			}
		}
	}
	if res.Batch == nil || res.Batch.SettledTx != o.TxID || len(res.Batch.TradeIDs) != 3 {
		t.Fatalf("batch record not read back from the ledger: %+v", res.Batch)
	}
	for _, id := range ids {
		var v struct {
			Trade struct {
				Status     string `json:"status"`
				SettledVia string `json:"settledVia"`
				SettledTx  string `json:"settledTx"`
			} `json:"trade"`
		}
		call(t, "GET", "/api/trades/"+id, nil, &v)
		if v.Trade.Status != "SETTLED" || v.Trade.SettledVia != "NET:"+res.BatchID || v.Trade.SettledTx != o.TxID {
			t.Fatalf("%s: %+v, want SETTLED via NET:%s in %s", id, v.Trade, res.BatchID, o.TxID)
		}
	}
	t.Logf("net-settled %s in tx %s block %d: USD %d BANKFX->BANKIN, INR %d BANKIN->BANKFX (gross USD %d)",
		res.BatchID, o.TxID, o.BlockNumber, wantUSD, wantINR, 650_00)
	assertLedgerSane(t, c)

	// A batch containing an already-settled trade is refused whole.
	e, f := tradeID("IT-NET-D"), tradeID("IT-NET-E")
	instructBoth(t, e, "BANKFX", 100_00)
	instructBoth(t, f, "BANKFX", 100_00)
	var bad netSettleResp
	call(t, "POST", "/api/net-settle", map[string]any{"as": "BANKIN", "tradeIds": []string{e, f, a}}, &bad)
	bo := bad.Result.Outcome
	if bo.OK || bo.Code != "ERR_ALREADY_SETTLED" {
		t.Fatalf("batch with a settled trade: want ERR_ALREADY_SETTLED, got ok=%v %s %s", bo.OK, bo.Code, bo.Message)
	}
	if bad.Result.BalancesMoved {
		t.Fatal("balances moved on a refused batch")
	}
	for _, id := range []string{e, f} {
		var v struct {
			Trade struct {
				Status string `json:"status"`
			} `json:"trade"`
		}
		call(t, "GET", "/api/trades/"+id, nil, &v)
		if v.Trade.Status != "MATCHED" {
			t.Fatalf("%s is %s after a refused batch, want MATCHED", id, v.Trade.Status)
		}
	}
	// The preview of that batch is refused by the chaincode too (HTTP 422).
	raw, _ := json.Marshal(map[string]any{"tradeIds": []string{e, f, a}})
	resp, err := http.Post(baseURL+"/api/net-preview", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("ERR_ALREADY_SETTLED")) {
		t.Fatalf("preview of a bad batch: want 422 ERR_ALREADY_SETTLED, got %d %s", resp.StatusCode, body)
	}
	assertLedgerSane(t, c)
}
