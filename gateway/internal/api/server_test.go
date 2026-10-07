package api

import (
	"errors"
	"net/http"
	"testing"
)

func TestTradeReadStatus(t *testing.T) {
	tests := []struct {
		err  string
		want int
	}{
		{"ERR_TRADE_NOT_FOUND: no trade T-1", http.StatusNotFound},
		{`ERR_INVALID_INPUT: tradeId "a b" must be 1-64 chars of [A-Za-z0-9._-]`, http.StatusBadRequest},
		{"ENDORSE_FAILED: rpc error: code = Unavailable", http.StatusBadGateway},
		{"ERR_INTERNAL: scan TRADE: boom", http.StatusBadGateway},
	}
	for _, tt := range tests {
		if got := tradeReadStatus(errors.New(tt.err)); got != tt.want {
			t.Errorf("tradeReadStatus(%q) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func TestChaincodeStatus(t *testing.T) {
	if got := chaincodeStatus(errors.New("ERR_ALREADY_SETTLED: batch B1: trade T1 was already settled")); got != http.StatusUnprocessableEntity {
		t.Errorf("chaincode refusal: got %d, want 422", got)
	}
	if got := chaincodeStatus(errors.New("ENDORSE_FAILED: rpc error: code = Unavailable")); got != http.StatusBadGateway {
		t.Errorf("transport failure: got %d, want 502", got)
	}
}

func TestNetRequestJSON(t *testing.T) {
	got := netRequestJSON("B1", []string{"T1", "T2"})
	if want := `{"batchId":"B1","tradeIds":["T1","T2"]}`; got != want {
		t.Errorf("netRequestJSON = %s, want %s", got, want)
	}
}

// A balance change on a simulated ledger-level bank (BANKUS/BANKSG) must count
// as balances moving, not only changes on BANKIN/BANKFX.
func TestSameBalancesCoversAllLedgerBanks(t *testing.T) {
	base := func() *Snapshot {
		s := &Snapshot{Balances: map[string]map[string]int64{}}
		for _, b := range ledgerBanks {
			s.Balances[b] = map[string]int64{"INR": 100, "USD": 100}
		}
		return s
	}
	if !sameBalances(base(), base()) {
		t.Fatal("identical snapshots reported as different")
	}
	for _, b := range ledgerBanks {
		for _, ccy := range []string{"INR", "USD"} {
			after := base()
			after.Balances[b][ccy]++
			if sameBalances(base(), after) {
				t.Fatalf("change on %s/%s not detected", b, ccy)
			}
		}
	}
}
