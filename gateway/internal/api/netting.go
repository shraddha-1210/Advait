package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
)

// NetBody is the request for both netting endpoints.
type NetBody struct {
	As       string   `json:"as,omitempty"`      // net-settle only: BANKIN or BANKFX
	BatchID  string   `json:"batchId,omitempty"` // optional; generated if empty
	TradeIDs []string `json:"tradeIds"`
}

// NetSettleResponse is the real result of a net settlement.
type NetSettleResponse struct {
	BatchID string       `json:"batchId"`
	Result  *WriteResult `json:"result"`
	// The on-ledger batch record (chaincode GetBatch), only when committed.
	Batch json.RawMessage `json:"batch,omitempty"`
}

func netRequestJSON(batchID string, ids []string) string {
	raw, _ := json.Marshal(map[string]any{"batchId": batchID, "tradeIds": ids})
	return string(raw)
}

// chaincodeStatus maps a query failure to an HTTP status: a chaincode
// refusal (ERR_...) is a client-visible verdict, anything else is the
// gateway failing to get an answer.
func chaincodeStatus(err error) int {
	if strings.Contains(err.Error(), "ERR_") {
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadGateway
}

// netPreview returns gross vs net figures for a batch, computed by the
// chaincode's PreviewNet (read-only, same arithmetic as NetSettle).
func (s *Server) netPreview(w http.ResponseWriter, r *http.Request) {
	var body NetBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := body.BatchID
	if id == "" {
		id = "PREVIEW"
	}
	out, err := s.L.Evaluate(ledger.BankIN, "PreviewNet", netRequestJSON(id, body.TradeIDs))
	if err != nil {
		writeErr(w, chaincodeStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, rawJSON(out))
}

// netSettle submits NetSettle. The response carries the real outcome and the
// real balance reads before and after; on success, the batch record as the
// ledger stored it.
func (s *Server) netSettle(w http.ResponseWriter, r *http.Request) {
	var body NetBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := parseParty(body.As)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := body.BatchID
	if id == "" {
		id = "NET-" + strings.ToUpper(strconv.FormatInt(time.Now().UnixNano()%1e12, 36))
	}
	res, err := s.submitWithSnapshots(p, "NetSettle", []string{netRequestJSON(id, body.TradeIDs)}, ledger.SubmitOptions{}, "",
		fmt.Sprintf("%s net-settles batch %s of %d trades", p, id, len(body.TradeIDs)))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	resp := NetSettleResponse{BatchID: id, Result: res}
	if res.Outcome.OK {
		if b, err := s.L.Evaluate(ledger.BankIN, "GetBatch", id); err == nil {
			resp.Batch = rawJSON(b)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
