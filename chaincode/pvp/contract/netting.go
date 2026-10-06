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

// computeNet nets a batch of trades across all participant banks multilaterally.
// It is pure: it reads nothing and writes nothing, so PreviewNet and NetSettle use exactly the same arithmetic.
func computeNet(batchID string, trades []*Trade) (*NetPlan, error) {
	plan := &NetPlan{
		BatchID:      batchID,
		Gross:        map[string]int64{INR: 0, USD: 0},
		GrossByPayer: map[string]map[string]int64{},
	}
	for _, b := range allBanks {
		plan.GrossByPayer[b] = map[string]int64{INR: 0, USD: 0}
	}

	grossReceived := map[string]map[string]int64{}
	for _, b := range allBanks {
		grossReceived[b] = map[string]int64{INR: 0, USD: 0}
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

			r, err := addChecked(grossReceived[l.To][l.Ccy], l.Amount)
			if err != nil {
				return nil, err
			}
			grossReceived[l.To][l.Ccy] = r
		}
	}
	sort.Strings(plan.TradeIDs)

	// Multilateral net positions per currency.
	for _, ccy := range allCurrencies {
		netPos := map[string]int64{}
		for _, b := range allBanks {
			netPos[b] = grossReceived[b][ccy] - plan.GrossByPayer[b][ccy]
		}

		type bankAmt struct {
			bank string
			amt  int64
		}
		var debtors []bankAmt
		var creditors []bankAmt
		for _, b := range allBanks {
			if netPos[b] < 0 {
				debtors = append(debtors, bankAmt{bank: b, amt: -netPos[b]})
			} else if netPos[b] > 0 {
				creditors = append(creditors, bankAmt{bank: b, amt: netPos[b]})
			}
		}

		i, j := 0, 0
		legsEmitted := 0
		for i < len(debtors) && j < len(creditors) {
			transfer := debtors[i].amt
			if creditors[j].amt < transfer {
				transfer = creditors[j].amt
			}
			if transfer > 0 {
				plan.Net = append(plan.Net, NetLeg{Currency: ccy, From: debtors[i].bank, To: creditors[j].bank, Amount: transfer})
				debtors[i].amt -= transfer
				creditors[j].amt -= transfer
				legsEmitted++
			}
			if debtors[i].amt == 0 {
				i++
			}
			if creditors[j].amt == 0 {
				j++
			}
		}
		if legsEmitted == 0 {
			plan.Net = append(plan.Net, NetLeg{Currency: ccy, Amount: 0})
		}
	}
	return plan, nil
}

// Cycle represents a detected circular chain of payment obligations in one currency.
type Cycle struct {
	Currency   string   `json:"currency"`
	Path       []string `json:"path"`       // e.g. ["BANKIN", "BANKFX", "BANKUS", "BANKIN"]
	Bottleneck int64    `json:"bottleneck"` // maximum capacity that can be offset along cycle
}

// DetectCycles finds all simple circular payment obligations across trades deterministically.
func DetectCycles(trades []*Trade) []Cycle {
	var cycles []Cycle

	for _, ccy := range allCurrencies {
		matrix := map[string]map[string]int64{}
		for _, b1 := range allBanks {
			matrix[b1] = map[string]int64{}
			for _, b2 := range allBanks {
				matrix[b1][b2] = 0
			}
		}
		for _, t := range trades {
			legs := []leg{
				{From: t.USDDeliverer, To: t.INRDeliverer, Ccy: USD, Amount: t.USDAmount},
				{From: t.INRDeliverer, To: t.USDDeliverer, Ccy: INR, Amount: t.INRAmount},
			}
			for _, l := range legs {
				if l.Ccy == ccy {
					matrix[l.From][l.To] += l.Amount
				}
			}
		}

		for {
			foundCycle := false
			for _, startBank := range allBanks {
				visited := map[string]bool{}
				path := []string{startBank}
				visited[startBank] = true

				var dfs func(curr string) bool
				dfs = func(curr string) bool {
					for _, nextBank := range allBanks {
						if matrix[curr][nextBank] > 0 {
							if nextBank == startBank && len(path) >= 2 {
								pathWithStart := append([]string(nil), path...)
								pathWithStart = append(pathWithStart, startBank)

								bottleneck := matrix[path[0]][path[1]]
								for idx := 0; idx < len(path)-1; idx++ {
									u, v := path[idx], path[idx+1]
									if matrix[u][v] < bottleneck {
										bottleneck = matrix[u][v]
									}
								}
								u, v := path[len(path)-1], startBank
								if matrix[u][v] < bottleneck {
									bottleneck = matrix[u][v]
								}

								if bottleneck > 0 {
									cycles = append(cycles, Cycle{
										Currency:   ccy,
										Path:       pathWithStart,
										Bottleneck: bottleneck,
									})
									for idx := 0; idx < len(path)-1; idx++ {
										matrix[path[idx]][path[idx+1]] -= bottleneck
									}
									matrix[path[len(path)-1]][startBank] -= bottleneck
									foundCycle = true
									return true
								}
							} else if !visited[nextBank] {
								visited[nextBank] = true
								path = append(path, nextBank)
								if dfs(nextBank) {
									return true
								}
								path = path[:len(path)-1]
								visited[nextBank] = false
							}
						}
					}
					return false
				}

				if dfs(startBank) {
					break
				}
			}
			if !foundCycle {
				break
			}
		}
	}
	return cycles
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

// computeMultiNet recomputes all positions directly from stored matched trades.
// Client-provided totals are never trusted.
func computeMultiNet(batchID string, trades []*Trade) (*NetPlan, error) {
	return computeNet(batchID, trades)
}

// resolveGridlock iteratively removes the largest outgoing trade of an underfunded bank
// until the remaining trade candidate set is fully funded and resolvable, or no trades remain.
func resolveGridlock(stub shim.ChaincodeStubInterface, cfg *Config, batchID string, inputTrades []*Trade, before Balances) (*LiquidityPlan, Balances, error) {
	inputIDs := make([]string, len(inputTrades))
	for i, t := range inputTrades {
		inputIDs[i] = t.TradeID
	}
	sort.Strings(inputIDs)

	current := append([]*Trade{}, inputTrades...)

	for {
		if len(current) == 0 {
			return nil, nil, reject(ErrInsufficientFunds, "gridlock resolution for batch %s: no trades can be settled with available funds", batchID)
		}

		plan, err := computeNet(batchID, current)
		if err != nil {
			return nil, nil, err
		}

		post, applyErr := applyLegs(before, netLegs(plan), "liquidity batch "+batchID)
		if applyErr == nil {
			settledIDs := make([]string, len(current))
			for i, t := range current {
				settledIDs[i] = t.TradeID
			}
			sort.Strings(settledIDs)

			settledSet := map[string]bool{}
			for _, id := range settledIDs {
				settledSet[id] = true
			}

			var droppedIDs []string
			for _, id := range inputIDs {
				if !settledSet[id] {
					droppedIDs = append(droppedIDs, id)
				}
			}
			sort.Strings(droppedIDs)

			cycles := DetectCycles(current)

			resPlan := &LiquidityPlan{
				BatchID:         batchID,
				InputTradeIDs:   inputIDs,
				SettledTradeIDs: settledIDs,
				DroppedTradeIDs: droppedIDs,
				Cycles:          cycles,
				NetPlan:         *plan,
			}
			return resPlan, post, nil
		}

		// Underfunded! Identify short banks and currencies
		grossPaid := map[string]map[string]int64{}
		grossReceived := map[string]map[string]int64{}
		for _, b := range allBanks {
			grossPaid[b] = map[string]int64{INR: 0, USD: 0}
			grossReceived[b] = map[string]int64{INR: 0, USD: 0}
		}

		for _, t := range current {
			grossPaid[t.USDDeliverer][USD] += t.USDAmount
			grossReceived[t.INRDeliverer][USD] += t.USDAmount

			grossPaid[t.INRDeliverer][INR] += t.INRAmount
			grossReceived[t.USDDeliverer][INR] += t.INRAmount
		}

		isShort := map[string]map[string]bool{
			BankIN: {INR: false, USD: false},
			BankFX: {INR: false, USD: false},
			BankUS: {INR: false, USD: false},
			BankSG: {INR: false, USD: false},
		}

		anyShort := false
		for _, b := range allBanks {
			for _, ccy := range allCurrencies {
				owed := grossPaid[b][ccy] - grossReceived[b][ccy]
				if owed > 0 && before.get(b, ccy) < owed {
					isShort[b][ccy] = true
					anyShort = true
				}
			}
		}

		if !anyShort || len(current) == 1 {
			return nil, nil, applyErr
		}

		type candidateRemoval struct {
			tradeIndex int
			trade      *Trade
			ccy        string
			amount     int64
		}

		var candidates []candidateRemoval

		for idx, t := range current {
			if isShort[t.USDDeliverer][USD] {
				candidates = append(candidates, candidateRemoval{
					tradeIndex: idx,
					trade:      t,
					ccy:        USD,
					amount:     t.USDAmount,
				})
			}
			if isShort[t.INRDeliverer][INR] {
				candidates = append(candidates, candidateRemoval{
					tradeIndex: idx,
					trade:      t,
					ccy:        INR,
					amount:     t.INRAmount,
				})
			}
		}

		if len(candidates) == 0 {
			return nil, nil, applyErr
		}

		// Sort candidate removals by:
		// 1. Largest trade amount descending
		// 2. Currency ascending (USD before INR)
		// 3. Trade ID ascending
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].amount != candidates[j].amount {
				return candidates[i].amount > candidates[j].amount
			}
			if candidates[i].ccy != candidates[j].ccy {
				return candidates[i].ccy < candidates[j].ccy
			}
			return candidates[i].trade.TradeID < candidates[j].trade.TradeID
		})

		removeID := candidates[0].trade.TradeID
		nextCurrent := make([]*Trade, 0, len(current)-1)
		removed := false
		for _, t := range current {
			if !removed && t.TradeID == removeID {
				removed = true
				continue
			}
			nextCurrent = append(nextCurrent, t)
		}
		current = nextCurrent
	}
}

func loadLiquidityBatch(stub shim.ChaincodeStubInterface, cfg *Config, raw string) (*LiquidityRequest, []*Trade, map[string]string, error) {
	var req LiquidityRequest
	if err := decodeStrict("liquidity request", raw, &req); err != nil {
		return nil, nil, nil, err
	}
	if err := validID("batchId", req.BatchID); err != nil {
		return nil, nil, nil, err
	}
	if len(req.TradeIDs) < 1 {
		return nil, nil, nil, reject(ErrBatch, "a liquidity batch needs at least 1 trade ID, got 0")
	}
	if len(req.TradeIDs) > MaxBatchSize {
		return nil, nil, nil, reject(ErrBatch, "a liquidity batch holds at most %d trades, got %d", MaxBatchSize, len(req.TradeIDs))
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
	keysMap := map[string]string{}
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
				"batch %s: trade %s was already settled in tx %s; the batch is refused", req.BatchID, id, t.SettledTx)
		case StatusPendingMatch:
			return nil, nil, nil, reject(ErrUnilateral,
				"batch %s: trade %s has only %s's instruction; the batch is refused", req.BatchID, id, strings.Join(t.InstructedBy, ","))
		case StatusMatched:
		default:
			return nil, nil, nil, reject(ErrInternal, "trade %s has unknown status %q", id, t.Status)
		}
		if _, err := checkRateUsable(stub, cfg, t.RateSeq, t.USDAmount, t.INRAmount); err != nil {
			return nil, nil, nil, err
		}
		trades = append(trades, t)
		keysMap[id] = k
	}
	return &req, trades, keysMap, nil
}

// PreviewLiquidity computes gridlock resolution and netting preview without settling.
func (c *PvPContract) PreviewLiquidity(ctx contractapi.TransactionContextInterface, requestJSON string) (string, error) {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return "", err
	}
	req, trades, _, err := loadLiquidityBatch(stub, cfg, requestJSON)
	if err != nil {
		return "", err
	}
	before, _, err := readAllBalances(stub)
	if err != nil {
		return "", err
	}
	plan, _, err := resolveGridlock(stub, cfg, req.BatchID, trades, before)
	fundsOK, fundsMsg := true, ""
	if err != nil {
		fundsOK, fundsMsg = false, err.Error()
	}
	return toJSON(map[string]any{"plan": plan, "fundsOk": fundsOK, "fundsMessage": fundsMsg})
}

// LiquidityResolve resolves gridlock and settles the candidate trades atomically.
func (c *PvPContract) LiquidityResolve(ctx contractapi.TransactionContextInterface, requestJSON string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	_, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return err
	}
	req, trades, keysMap, err := loadLiquidityBatch(stub, cfg, requestJSON)
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

	before, _, err := readAllBalances(stub)
	if err != nil {
		return err
	}
	plan, post, err := resolveGridlock(stub, cfg, req.BatchID, trades, before)
	if err != nil {
		return err
	}

	if len(plan.SettledTradeIDs) == 0 {
		return reject(ErrGridlock, "gridlock resolution for batch %s resulted in 0 resolvable trades", req.BatchID)
	}

	if err := assertConservation(stub, post); err != nil {
		return err
	}

	if err := writeBalances(stub, post); err != nil {
		return err
	}
	txID := stub.GetTxID()
	for _, id := range plan.SettledTradeIDs {
		k := keysMap[id]
		var t Trade
		found, err := getJSON(stub, k, &t)
		if err != nil || !found {
			return reject(ErrInternal, "load trade %s: %v", id, err)
		}
		t.Status = StatusSettled
		t.SettledTx = txID
		t.SettledVia = "LIQUIDITY:" + req.BatchID
		if err := putJSON(stub, k, t); err != nil {
			return err
		}
	}

	after := before.clone()
	for bank, m := range post {
		for ccy, v := range m {
			after.set(bank, ccy, v)
		}
	}

	batch := Batch{
		NetPlan:        plan.NetPlan,
		SettledTx:      txID,
		SubmittedBy:    msp,
		BalancesBefore: snapshot(before),
		BalancesAfter:  snapshot(after),
	}
	if err := putJSON(stub, bk, batch); err != nil {
		return err
	}
	if err := setEvent(stub, "LiquiditySettled", map[string]any{
		"batchId": req.BatchID,
		"txId":    txID,
		"settled": len(plan.SettledTradeIDs),
		"dropped": len(plan.DroppedTradeIDs),
	}); err != nil {
		return err
	}

	return appendLog(stub, "LIQUIDITY_RESOLVED", msp, req.BatchID,
		fmt.Sprintf("gridlock resolved for batch %s: %d of %d trades settled atomically (%d dropped); settled trades: %s",
			req.BatchID, len(plan.SettledTradeIDs), len(plan.InputTradeIDs), len(plan.DroppedTradeIDs), strings.Join(plan.SettledTradeIDs, ",")))
}
