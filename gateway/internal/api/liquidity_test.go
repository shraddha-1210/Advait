package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
)

func post(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return w
}

// A client cannot send net figures, savings or a settled set: the gateway
// refuses the body before anything reaches the ledger (s.L is nil here, so
// reaching the ledger would panic).
func TestLiquidityEndpointsRefuseClientFigures(t *testing.T) {
	s := &Server{}
	bodies := []string{
		`{"as":"BANKIN","tradeIds":["T1"],"net":[{"currency":"USD","amount":1}]}`,
		`{"as":"BANKIN","tradeIds":["T1"],"savingsPct":90}`,
		`{"as":"BANKIN","tradeIds":["T1"],"settledTradeIds":["T1"]}`,
		`{"as":"BANKIN","tradeIds":["T1"],"gross":{"USD":0}}`,
	}
	for _, b := range bodies {
		for name, h := range map[string]http.HandlerFunc{"settle": s.liquiditySettle, "preview": s.liquidityPreview} {
			if w := post(h, b); w.Code != http.StatusBadRequest {
				t.Fatalf("%s %s: status %d, want 400", name, b, w.Code)
			}
		}
	}
	if w := post(s.liquidityResolve, `{"tradeIds":["T1"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("resolve must not accept trade IDs: status %d", w.Code)
	}
}

func TestLiquiditySettleNeedsABankOrg(t *testing.T) {
	s := &Server{}
	for _, as := range []string{"", "AUDITOR", "ORACLE", "BANKUS", "BANKSG"} {
		w := post(s.liquiditySettle, `{"as":"`+as+`","tradeIds":["T1"]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("as=%q: status %d, want 400", as, w.Code)
		}
	}
	if w := post(s.liquidityPreview, `{"as":"BANKIN","tradeIds":["T1"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("preview with a submitting org: status %d, want 400", w.Code)
	}
}

func TestLiquidityRequestJSONCarriesOnlyIDs(t *testing.T) {
	got := liquidityRequestJSON("B1", []string{"T2", "T1"})
	if want := `{"batchId":"B1","tradeIds":["T2","T1"]}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestMatchedTradeIDs(t *testing.T) {
	raw := []byte(`[
		{"tradeId":"C","status":"MATCHED"},
		{"tradeId":"A","status":"MATCHED"},
		{"tradeId":"B","status":"SETTLED"},
		{"tradeId":"D","status":"PENDING_MATCH"},
		{"tradeId":"E","status":"MATCHED"}
	]`)
	ids, matched, truncated, err := matchedTradeIDs(raw, 50)
	if err != nil || !reflect.DeepEqual(ids, []string{"A", "C", "E"}) || matched != 3 || truncated {
		t.Fatalf("got %v %d %v %v", ids, matched, truncated, err)
	}
	ids, matched, truncated, _ = matchedTradeIDs(raw, 2)
	if !reflect.DeepEqual(ids, []string{"A", "C"}) || matched != 3 || !truncated {
		t.Fatalf("capped: got %v %d %v", ids, matched, truncated)
	}
	if _, _, _, err := matchedTradeIDs([]byte(`{`), 50); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestCustodies(t *testing.T) {
	yes := map[ledger.Party][]string{
		ledger.BankIN: {"BANKIN", "bankus", "BANKUS"},
		ledger.BankFX: {"BANKFX", "BANKSG"},
	}
	no := map[ledger.Party][]string{
		ledger.BankIN: {"BANKFX", "BANKSG", "AUDITOR", ""},
		ledger.BankFX: {"BANKIN", "BANKUS", "ORACLE"},
	}
	for p, bs := range yes {
		for _, b := range bs {
			if !custodies(p, b) {
				t.Fatalf("%s should custody %s", p, b)
			}
		}
	}
	for p, bs := range no {
		for _, b := range bs {
			if custodies(p, b) {
				t.Fatalf("%s must not custody %q", p, b)
			}
		}
	}
}

// The normal instruction endpoint refuses to instruct as another org's bank
// before reaching the ledger.
func TestInstructRefusesAnotherOrgsBank(t *testing.T) {
	s := &Server{}
	for _, b := range []string{
		`{"as":"BANKFX","asBank":"BANKIN","tradeId":"T1","usdDeliverer":"BANKFX","usdAmount":"1","inrAmount":"1","rateSeq":1}`,
		`{"as":"BANKIN","asBank":"BANKSG","tradeId":"T1","usdDeliverer":"BANKUS","inrDeliverer":"BANKSG","usdAmount":"1","inrAmount":"1","rateSeq":1}`,
	} {
		if w := post(s.instruct, b); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", b, w.Code)
		}
	}
}

func TestInstructionJSONInrDeliverer(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal([]byte(instructionJSON(InstructionBody{As: "BANKIN", TradeID: "T1", USDDeliverer: "BANKFX"})), &m)
	if _, ok := m["inrDeliverer"]; ok {
		t.Fatal("two-bank instruction must not carry inrDeliverer (chaincode defaults it)")
	}
	_ = json.Unmarshal([]byte(instructionJSON(InstructionBody{As: "BANKIN", AsBank: "BANKUS", TradeID: "T1", USDDeliverer: "BANKUS", INRDeliverer: "BANKSG"})), &m)
	if m["inrDeliverer"] != "BANKSG" || m["asBank"] != "BANKUS" {
		t.Fatalf("four-bank instruction: %v", m)
	}
}
