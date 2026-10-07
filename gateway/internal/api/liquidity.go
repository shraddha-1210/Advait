package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
)

// Liquidity engine endpoints. The gateway only passes trade IDs to the
// chaincode and returns what the chaincode computed: it never calculates or
// adjusts a net position, a dropped trade, a cycle or a saving itself. The
// request bodies accept no such fields (unknown fields are refused).

// liquidityMaxBatch mirrors the chaincode's MaxBatchSize. The chaincode
// enforces it; the gateway uses it only to pick resolve candidates.
const liquidityMaxBatch = 50

// LiquidityBody is the request for preview and settle.
type LiquidityBody struct {
	As       string   `json:"as,omitempty"`      // settle only: BANKIN or BANKFX (the submitting org)
	BatchID  string   `json:"batchId,omitempty"` // optional; generated if empty
	TradeIDs []string `json:"tradeIds"`
}

// ResolveBody is the request for resolve: it names no trades.
type ResolveBody struct {
	BatchID string `json:"batchId,omitempty"`
}

// LiquiditySettleResponse is the real result of a liquidity settlement.
type LiquiditySettleResponse struct {
	BatchID string       `json:"batchId"`
	Result  *WriteResult `json:"result"`
	// The on-ledger batch record (chaincode GetBatch), only when committed.
	Batch json.RawMessage `json:"batch,omitempty"`
}

// ResolveResponse lists the MATCHED trades the gateway found and the
// chaincode's preview of settling them together.
type ResolveResponse struct {
	BatchID   string          `json:"batchId"`
	TradeIDs  []string        `json:"tradeIds"`
	Matched   int             `json:"matched"`   // MATCHED trades on the ledger
	Truncated bool            `json:"truncated"` // more than liquidityMaxBatch were found
	Preview   json.RawMessage `json:"preview"`
}

func liquidityRequestJSON(batchID string, ids []string) string {
	raw, _ := json.Marshal(map[string]any{"batchId": batchID, "tradeIds": ids})
	return string(raw)
}

// matchedTradeIDs returns the IDs of MATCHED trades in a GetTrades result,
// sorted, capped at max (truncated reports whether any were left out).
func matchedTradeIDs(getTrades []byte, max int) (ids []string, matched int, truncated bool, err error) {
	var trades []struct {
		TradeID string `json:"tradeId"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(getTrades, &trades); err != nil {
		return nil, 0, false, fmt.Errorf("decode GetTrades: %w", err)
	}
	for _, t := range trades {
		if t.Status == "MATCHED" {
			ids = append(ids, t.TradeID)
		}
	}
	sort.Strings(ids)
	matched = len(ids)
	if len(ids) > max {
		ids, truncated = ids[:max], true
	}
	return ids, matched, truncated, nil
}

func newBatchID(prefix string) string {
	return prefix + "-" + strings.ToUpper(strconv.FormatInt(time.Now().UnixNano()%1e12, 36))
}

// liquidityPreview returns the chaincode's PreviewLiquidity for the listed
// trades (read-only, same validation and resolver as LiquiditySettle).
func (s *Server) liquidityPreview(w http.ResponseWriter, r *http.Request) {
	var body LiquidityBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.As != "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("preview takes no submitting org"))
		return
	}
	id := body.BatchID
	if id == "" {
		id = "PREVIEW"
	}
	out, err := s.L.Evaluate(ledger.BankIN, "PreviewLiquidity", liquidityRequestJSON(id, body.TradeIDs))
	if err != nil {
		writeErr(w, chaincodeStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, rawJSON(out))
}

// liquidityResolve finds every MATCHED trade (up to the batch limit, lowest
// trade IDs first) and returns the chaincode's preview of settling them
// together. It is read-only: settling is a separate, explicit call with the
// trade IDs returned here.
func (s *Server) liquidityResolve(w http.ResponseWriter, r *http.Request) {
	var body ResolveBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := body.BatchID
	if id == "" {
		id = newBatchID("LIQ")
	}
	trades, err := s.L.Evaluate(ledger.BankIN, "GetTrades")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	ids, matched, truncated, err := matchedTradeIDs(trades, liquidityMaxBatch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	resp := ResolveResponse{BatchID: id, TradeIDs: ids, Matched: matched, Truncated: truncated, Preview: rawJSON(nil)}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	out, err := s.L.Evaluate(ledger.BankIN, "PreviewLiquidity", liquidityRequestJSON(id, ids))
	if err != nil {
		writeErr(w, chaincodeStatus(err), err)
		return
	}
	resp.Preview = rawJSON(out)
	writeJSON(w, http.StatusOK, resp)
}

// liquiditySettle submits LiquiditySettle for the listed trades. The
// response carries the real outcome, real balance reads before and after,
// and on success the batch record as the ledger stored it.
func (s *Server) liquiditySettle(w http.ResponseWriter, r *http.Request) {
	var body LiquidityBody
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
		id = newBatchID("LIQ")
	}
	res, err := s.submitWithSnapshots(p, "LiquiditySettle", []string{liquidityRequestJSON(id, body.TradeIDs)}, ledger.SubmitOptions{}, "",
		fmt.Sprintf("%s liquidity-settles batch %s of %d trades", p, id, len(body.TradeIDs)))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	resp := LiquiditySettleResponse{BatchID: id, Result: res}
	if res.Outcome.OK {
		if b, err := s.L.Evaluate(ledger.BankIN, "GetBatch", id); err == nil {
			resp.Batch = rawJSON(b)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
