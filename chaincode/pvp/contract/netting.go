package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Bilateral netting.
//
// A batch of MATCHED trades between BankIN and BankFX settles as one net
// movement per currency instead of every trade's gross legs. Everything
// happens in ONE transaction: every trade is checked, funds are checked
// against the NET position, the invariant runs on the result, and only then
// are balances, all trade records, the batch record and one audit entry
// written. Any failure rejects the whole batch and nothing is written.
//
// Bilateral only: with exactly two banks, the net per currency is a single
// amount flowing one way.

// MaxBatchSize bounds one batch so a transaction stays small.
const MaxBatchSize = 50

// NetRequest is the NetSettle / PreviewNet argument.
type NetRequest struct {
	BatchID  string   `json:"batchId"`
	TradeIDs []string `json:"tradeIds"`
}

// NetLeg is the single net movement of one currency. Amount 0 means the
// batch nets to zero in that currency and nothing moves.
type NetLeg struct {
	Currency string `json:"currency"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Amount   int64  `json:"amount"`
}

// NetPlan is the result of netting a batch: what would move gross (every
// trade settled one by one) and what moves net.
type NetPlan struct {
	BatchID  string   `json:"batchId"`
	TradeIDs []string `json:"tradeIds"` // sorted
	// Gross: total paid in each currency if every trade settled on its own.
	Gross map[string]int64 `json:"gross"`
	// Gross per payer: bank -> currency -> amount that bank would pay gross.
	GrossByPayer map[string]map[string]int64 `json:"grossByPayer"`
	Net          []NetLeg                    `json:"net"` // one entry per currency, INR then USD
}

// Batch is the on-ledger record of a settled net batch.
type Batch struct {
	NetPlan
	SettledTx   string `json:"settledTx"`
	SubmittedBy string `json:"submittedBy"`
	// Real balances immediately before and after the net settlement.
	BalancesBefore map[string]map[string]int64 `json:"balancesBefore"`
	BalancesAfter  map[string]map[string]int64 `json:"balancesAfter"`
}

// computeNet nets a batch of trades. It is pure: it reads nothing and writes
// nothing, so PreviewNet and NetSettle use exactly the same arithmetic.
func computeNet(batchID string, trades []*Trade) (*NetPlan, error) {
	plan := &NetPlan{
		BatchID:      batchID,
		Gross:        map[string]int64{INR: 0, USD: 0},
		GrossByPayer: map[string]map[string]int64{BankIN: {INR: 0, USD: 0}, BankFX: {INR: 0, USD: 0}},
	}
	for _, t := range trades {
		plan.TradeIDs = append(plan.TradeIDs, t.TradeID)
		for _, l := range []leg{
			{From: t.USDDeliverer, To: t.INRDeliverer, Ccy: USD, Amount: t.USDAmount},
			{From: t.INRDeliverer, To: t.USDDeliverer, Ccy: INR, Amount: t.INRAmount},
		} {
			if !isBank(l.From) || !isBank(l.To) || l.From == l.To || l.Amount <= 0 {
				return nil, reject(ErrInternal, "trade %s has an invalid %s leg", t.TradeID, l.Ccy)
			}
			g, err := addChecked(plan.Gross[l.Ccy], l.Amount)
			if err != nil {
				return nil, err
			}
			plan.Gross[l.Ccy] = g
			p, err := addChecked(plan.GrossByPayer[l.From][l.Ccy], l.Amount)
			if err != nil {
				return nil, err
			}
			plan.GrossByPayer[l.From][l.Ccy] = p
		}
	}
	sort.Strings(plan.TradeIDs)
	// Per currency: what BankIN owes BankFX minus what BankFX owes BankIN.
	for _, ccy := range allCurrencies {
		d, err := addChecked(plan.GrossByPayer[BankIN][ccy], -plan.GrossByPayer[BankFX][ccy])
		if err != nil {
			return nil, err
		}
		switch {
		case d > 0:
			plan.Net = append(plan.Net, NetLeg{Currency: ccy, From: BankIN, To: BankFX, Amount: d})
		case d < 0:
			plan.Net = append(plan.Net, NetLeg{Currency: ccy, From: BankFX, To: BankIN, Amount: -d})
		default:
			plan.Net = append(plan.Net, NetLeg{Currency: ccy, Amount: 0})
		}
	}
	return plan, nil
}

// loadBatch validates a NetRequest and loads its trades. Every trade must
// exist, be MATCHED (never settled, never one-sided) and still be priced at a
// usable attested rate. Checks are the same ones SettleTrade applies.
func loadBatch(stub shim.ChaincodeStubInterface, cfg *Config, raw string) (*NetRequest, []*Trade, []string, error) {
	var req NetRequest
	if err := decodeStrict("net request", raw, &req); err != nil {
		return nil, nil, nil, err
	}
	if err := validID("batchId", req.BatchID); err != nil {
		return nil, nil, nil, err
	}
	if len(req.TradeIDs) < 2 {
		return nil, nil, nil, reject(ErrBatch, "a net batch needs at least 2 trades, got %d", len(req.TradeIDs))
	}
	if len(req.TradeIDs) > MaxBatchSize {
		return nil, nil, nil, reject(ErrBatch, "a net batch holds at most %d trades, got %d", MaxBatchSize, len(req.TradeIDs))
	}
	seen := map[string]bool{}
	for _, id := range req.TradeIDs {
		if err := validID("tradeId", id); err != nil {
			return nil, nil, nil, err
		}
		if seen[id] {
			return nil, nil, nil, reject(ErrBatch, "trade %s appears twice in the batch", id)
		}
		seen[id] = true
	}
	ids := append([]string(nil), req.TradeIDs...)
	sort.Strings(ids)
	trades := make([]*Trade, 0, len(ids))
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		t, k, err := loadTrade(stub, id)
		if err != nil {
			return nil, nil, nil, err
		}
		if t == nil {
			return nil, nil, nil, reject(ErrTradeNotFound, "batch %s: no trade %s has been instructed", req.BatchID, id)
		}
		switch t.Status {
		case StatusSettled:
			return nil, nil, nil, reject(ErrAlreadySettled,
				"batch %s: trade %s was already settled in tx %s; the whole batch is refused", req.BatchID, id, t.SettledTx)
		case StatusPendingMatch:
			return nil, nil, nil, reject(ErrUnilateral,
				"batch %s: trade %s has only %s's instruction; the whole batch is refused", req.BatchID, id, strings.Join(t.InstructedBy, ","))
		case StatusMatched:
		default:
			return nil, nil, nil, reject(ErrInternal, "trade %s has unknown status %q", id, t.Status)
		}
		if _, err := checkRateUsable(stub, cfg, t.RateSeq, t.USDAmount, t.INRAmount); err != nil {
			return nil, nil, nil, err
		}
		trades = append(trades, t)
		keys = append(keys, k)
	}
	return &req, trades, keys, nil
}

// PreviewNet returns gross and net figures for a batch without settling it.
// Read-only. It runs the same validation and arithmetic as NetSettle, plus
// the funds check, so the preview says whether settlement would pass it.
func (c *PvPContract) PreviewNet(ctx contractapi.TransactionContextInterface, requestJSON string) (string, error) {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return "", err
	}
	req, trades, _, err := loadBatch(stub, cfg, requestJSON)
	if err != nil {
		return "", err
	}
	plan, err := computeNet(req.BatchID, trades)
	if err != nil {
		return "", err
	}
	before, _, err := readAllBalances(stub)
	if err != nil {
		return "", err
	}
	fundsOK, fundsMsg := true, ""
	if _, err := applyLegs(before, netLegs(plan), "batch "+plan.BatchID); err != nil {
		fundsOK, fundsMsg = false, err.Error()
	}
	return toJSON(map[string]any{"plan": plan, "fundsOk": fundsOK, "fundsMessage": fundsMsg})
}

// netLegs turns a plan into the non-zero movements applyLegs expects.
func netLegs(plan *NetPlan) []leg {
	var legs []leg
	for _, n := range plan.Net {
		if n.Amount > 0 {
			legs = append(legs, leg{From: n.From, To: n.To, Ccy: n.Currency, Amount: n.Amount})
		}
	}
	return legs
}

// NetSettle settles a batch of MATCHED trades as one net movement per
// currency, atomically. Either every trade in the batch settles and the net
// amounts move, or the transaction is refused and nothing is written.
func (c *PvPContract) NetSettle(ctx contractapi.TransactionContextInterface, requestJSON string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	_, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return err
	}
	req, trades, tradeKeys, err := loadBatch(stub, cfg, requestJSON)
	if err != nil {
		return err
	}
	bk, err := key(stub, keyBatch, req.BatchID)
	if err != nil {
		return err
	}
	var prior Batch
	if found, err := getJSON(stub, bk, &prior); err != nil {
		return err
	} else if found {
		return reject(ErrReplay, "batch %s already settled in tx %s; a batch ID cannot be reused", req.BatchID, prior.SettledTx)
	}
	plan, err := computeNet(req.BatchID, trades)
	if err != nil {
		return err
	}

	before, _, err := readAllBalances(stub)
	if err != nil {
		return err
	}
	// Funds are checked against the NET position only: that is the point of
	// netting. applyLegs refuses the batch if any net payer is short.
	post, err := applyLegs(before, netLegs(plan), "batch "+req.BatchID)
	if err != nil {
		return err
	}
	if err := assertConservation(stub, post); err != nil {
		return err
	}

	// Validation complete. Writes start here.
	if err := writeBalances(stub, post); err != nil {
		return err
	}
	txID := stub.GetTxID()
	for i, t := range trades {
		t.Status = StatusSettled
		t.SettledTx = txID
		t.SettledVia = "NET:" + req.BatchID
		if err := putJSON(stub, tradeKeys[i], t); err != nil {
			return err
		}
	}
	after := before.clone()
	for bank, m := range post {
		for ccy, v := range m {
			after.set(bank, ccy, v)
		}
	}
	batch := Batch{NetPlan: *plan, SettledTx: txID, SubmittedBy: msp, BalancesBefore: snapshot(before), BalancesAfter: snapshot(after)}
	if err := putJSON(stub, bk, batch); err != nil {
		return err
	}
	if err := setEvent(stub, "NetSettled", map[string]any{"batchId": req.BatchID, "txId": txID, "trades": len(trades)}); err != nil {
		return err
	}
	// One audit entry for the batch (a transaction cannot read its own
	// writes, so a second appendLog here would reuse the same sequence number).
	var net []string
	for _, n := range plan.Net {
		if n.Amount == 0 {
			net = append(net, fmt.Sprintf("%s nets to zero", n.Currency))
		} else {
			net = append(net, fmt.Sprintf("%s paid %d %s to %s", n.From, n.Amount, n.Currency, n.To))
		}
	}
	return appendLog(stub, "NET_SETTLED", msp, req.BatchID,
		fmt.Sprintf("%d trades settled atomically as one net movement per currency (gross %d INR paise, %d USD cents): %s; trades %s",
			len(trades), plan.Gross[INR], plan.Gross[USD], strings.Join(net, ", "), strings.Join(plan.TradeIDs, ",")))
}

// GetBatch returns one settled net batch.
func (c *PvPContract) GetBatch(ctx contractapi.TransactionContextInterface, batchID string) (string, error) {
	stub := ctx.GetStub()
	if err := validID("batchId", batchID); err != nil {
		return "", err
	}
	bk, err := key(stub, keyBatch, batchID)
	if err != nil {
		return "", err
	}
	var b Batch
	found, err := getJSON(stub, bk, &b)
	if err != nil {
		return "", err
	}
	if !found {
		return "", reject(ErrBatch, "no batch %s", batchID)
	}
	return toJSON(b)
}
