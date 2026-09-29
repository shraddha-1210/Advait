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
