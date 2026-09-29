// Package api is the HTTP API used by the frontend and the integration tests.
//
// Every number it returns comes from a chaincode query or a real transaction
// outcome. Nothing is computed or cached here except what the response says.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
)

type Server struct {
	L      *ledger.Client
	Oracle *oracle.Signer
	// Docker container names of each bank's endorsing (lite) peer, used by
	// the "bank offline" demo. Empty disables it.
	PeerContainers map[ledger.Party]string

	mu       sync.Mutex
	rejected []Rejection // off-ledger log of refused attempts (they never reach the ledger)
}

// Rejection is the gateway's record of a refused attempt. Refused
// transactions write nothing to the ledger by design, so this log is the
// only place they appear. It is kept in memory and is labelled as such.
type Rejection struct {
	At      string         `json:"at"`
	Attack  string         `json:"attack,omitempty"`
	Outcome ledger.Outcome `json:"outcome"`
	Attempt string         `json:"attempt"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/audit", s.audit)
	mux.HandleFunc("GET /api/quote", s.quote)
	mux.HandleFunc("GET /api/rejections", s.rejections)
	mux.HandleFunc("GET /api/trades/{id}", s.trade)
	mux.HandleFunc("POST /api/oracle/rates", s.publishRate)
	mux.HandleFunc("POST /api/instructions", s.instruct)
	mux.HandleFunc("POST /api/trades/{id}/settle", s.settle)
	mux.HandleFunc("POST /api/net-preview", s.netPreview)
	mux.HandleFunc("POST /api/net-settle", s.netSettle)
	mux.HandleFunc("GET /api/attacks", s.attackCatalogue)
	mux.HandleFunc("POST /api/attacks/{name}", s.runAttack)
	return cors(logging(mux))
}

// ---------------------------------------------------------------------------
// helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

func parseParty(s string) (ledger.Party, error) {
	switch ledger.Party(strings.ToUpper(s)) {
	case ledger.BankIN:
		return ledger.BankIN, nil
	case ledger.BankFX:
		return ledger.BankFX, nil
	}
	return "", fmt.Errorf("as must be BANKIN or BANKFX, got %q", s)
}

// Snapshot is a real GetBalances read.
type Snapshot struct {
	Balances     map[string]map[string]int64 `json:"balances"`
	Conservation []struct {
		Currency string `json:"currency"`
		Supply   int64  `json:"supply"`
		Sum      int64  `json:"sum"`
		Holds    bool   `json:"holds"`
	} `json:"conservation"`
	InvariantHolds bool   `json:"invariantHolds"`
	ReadAt         string `json:"readAt"`
}

func (s *Server) snapshot() (*Snapshot, error) {
	raw, err := s.L.Evaluate(ledger.BankIN, "GetBalances")
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	snap.InvariantHolds = len(snap.Conservation) > 0
	for _, c := range snap.Conservation {
		snap.InvariantHolds = snap.InvariantHolds && c.Holds
	}
	snap.ReadAt = time.Now().UTC().Format(time.RFC3339Nano)
	return &snap, nil
}

func sameBalances(a, b *Snapshot) bool {
	if a == nil || b == nil {
		return false
	}
	for _, bank := range []string{"BANKIN", "BANKFX"} {
		for _, ccy := range []string{"INR", "USD"} {
			if a.Balances[bank][ccy] != b.Balances[bank][ccy] {
				return false
			}
		}
	}
	return true
}

// WriteResult wraps a transaction outcome with real before/after reads.
type WriteResult struct {
	Outcome       ledger.Outcome `json:"outcome"`
	Before        *Snapshot      `json:"before"`
	After         *Snapshot      `json:"after"`
	BalancesMoved bool           `json:"balancesMoved"`
}

func (s *Server) submitWithSnapshots(p ledger.Party, fn string, args []string, opt ledger.SubmitOptions, attack, attempt string) (*WriteResult, error) {
	before, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	out := s.L.Submit(p, fn, args, opt)
	after, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	if !out.OK {
		s.recordRejection(attack, out, attempt)
	}
	return &WriteResult{Outcome: out, Before: before, After: after, BalancesMoved: !sameBalances(before, after)}, nil
}

func (s *Server) recordRejection(attack string, out ledger.Outcome, attempt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = append(s.rejected, Rejection{At: time.Now().UTC().Format(time.RFC3339), Attack: attack, Outcome: out, Attempt: attempt})
	if len(s.rejected) > 200 {
		s.rejected = s.rejected[len(s.rejected)-200:]
	}
}

func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// ---------------------------------------------------------------------------
// reads

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.L.Evaluate(ledger.BankIN, "GetConfig")
	resp := map[string]any{
		"parties": map[string]string{
			"BANKIN": s.L.MSPID(ledger.BankIN), "BANKFX": s.L.MSPID(ledger.BankFX),
			"ORACLE": s.L.MSPID(ledger.Oracle), "AUDITOR": s.L.MSPID(ledger.Auditor),
		},
		"oracle": s.Oracle.PublicKey(),
	}
	if err != nil {
		resp["ok"], resp["error"] = false, err.Error()
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	resp["ok"], resp["config"] = true, rawJSON(cfg)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	rates, err := s.L.Evaluate(ledger.BankIN, "GetRates")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	trades, err := s.L.Evaluate(ledger.BankIN, "GetTrades")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snap, "rates": rawJSON(rates), "trades": rawJSON(trades),
	})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	// Every read is made with the Auditor's own identity (AuditorMSP), a
	// channel member with no peer and no write rights in the chaincode.
	read := func(fn string) (json.RawMessage, error) {
		raw, err := s.L.Evaluate(ledger.Auditor, fn)
		if err != nil {
			return nil, err
		}
		return rawJSON(raw), nil
	}
	out := map[string]any{"queriedAs": "AUDITOR (" + s.L.MSPID(ledger.Auditor) + ")"}
	for key, fn := range map[string]string{"log": "GetAuditLog", "trades": "GetTrades", "rates": "GetRates", "invariant": "CheckInvariant"} {
		v, err := read(fn)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		out[key] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	usd, seq := r.URL.Query().Get("usd"), r.URL.Query().Get("seq")
	out, err := s.L.Evaluate(ledger.BankIN, "QuoteINR", usd, seq)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rawJSON(out))
}

// trade reads one trade and both banks' stored instructions with GetTrade,
// a point read of one key. The UI uses it to show a trade it just created or
// that a user opens by ID, without re-reading the whole list.
func (s *Server) trade(w http.ResponseWriter, r *http.Request) {
	out, err := s.L.Evaluate(ledger.BankIN, "GetTrade", r.PathValue("id"))
	if err != nil {
		writeErr(w, tradeReadStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, rawJSON(out))
}

// tradeReadStatus maps a GetTrade failure to an HTTP status: the chaincode's
// own "no such trade" and "bad id" refusals are client errors, anything else
// means the gateway could not get an answer from the ledger.
func tradeReadStatus(err error) int {
	switch msg := err.Error(); {
	case strings.Contains(msg, "ERR_TRADE_NOT_FOUND"):
		return http.StatusNotFound
	case strings.Contains(msg, "ERR_INVALID_INPUT"):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func (s *Server) rejections(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"note":       "Refused transactions write nothing to the ledger. This is the gateway's in-memory log of what the peers refused, newest last.",
		"rejections": s.rejected,
	})
}

// ---------------------------------------------------------------------------
// writes

func (s *Server) rateHead() (int64, error) {
	raw, err := s.L.Evaluate(ledger.BankIN, "GetRates")
	if err != nil {
		return 0, err
	}
	var v struct {
		Head int64 `json:"head"`
	}
	return v.Head, json.Unmarshal(raw, &v)
}

func (s *Server) publishRate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RateMicros int64 `json:"rateMicros"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	head, err := s.rateHead()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	att := s.Oracle.Sign(head+1, body.RateMicros, time.Now())
	raw, _ := json.Marshal(att)
	res, err := s.submitWithSnapshots(ledger.Oracle, "PublishRate", []string{string(raw)}, ledger.SubmitOptions{}, "",
		fmt.Sprintf("oracle publishes rate seq %d = %d micros", att.Seq, att.RateMicros))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attestation": att, "result": res})
}

// InstructionBody is what the UI sends. The instruction JSON passed to the
// chaincode is built from exactly these fields.
type InstructionBody struct {
	As           string `json:"as"`
	TradeID      string `json:"tradeId"`
	AsBank       string `json:"asBank,omitempty"` // defaults to As; set differently only by attacks
	USDDeliverer string `json:"usdDeliverer"`
	USDAmount    string `json:"usdAmount"`
	INRAmount    string `json:"inrAmount"`
	RateSeq      int64  `json:"rateSeq"`
}

func instructionJSON(b InstructionBody) string {
	asBank := b.AsBank
	if asBank == "" {
		asBank = strings.ToUpper(b.As)
	}
	raw, _ := json.Marshal(map[string]any{
		"tradeId": b.TradeID, "asBank": asBank, "usdDeliverer": b.USDDeliverer,
		"usdAmount": b.USDAmount, "inrAmount": b.INRAmount, "rateSeq": b.RateSeq,
	})
	return string(raw)
}

func (s *Server) instruct(w http.ResponseWriter, r *http.Request) {
	var body InstructionBody
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := parseParty(body.As)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	body.AsBank = "" // the normal endpoint always instructs as the submitter
	res, err := s.submitWithSnapshots(p, "SubmitInstruction", []string{instructionJSON(body)}, ledger.SubmitOptions{}, "",
		fmt.Sprintf("%s instructs trade %s", p, body.TradeID))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) settle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		As string `json:"as"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := parseParty(body.As)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	res, err := s.submitWithSnapshots(p, "SettleTrade", []string{id}, ledger.SubmitOptions{}, "",
		fmt.Sprintf("%s settles trade %s", p, id))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); strings.HasPrefix(o, "http://localhost:") || strings.HasPrefix(o, "http://127.0.0.1:") {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func logging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if r.Method != http.MethodGet {
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
