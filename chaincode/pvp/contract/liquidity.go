package contract

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Liquidity engine.
//
// LiquiditySettle takes an explicit list of MATCHED trade IDs and settles, net
// and in ONE transaction, the set of them that the chaincode can fund. The
// chaincode alone decides that set: it loads every listed trade from the
// ledger, nets them with computeNet, and while some bank cannot fund its net
// outflow it removes one trade by a fixed rule (resolveGridlock). Removed
// trades stay MATCHED and untouched. If no trade can be funded, the call is
// refused and nothing is written. The request carries trade IDs only; every
// amount, net position and cycle is recomputed from ledger state.
//
// Only a funding shortfall removes a trade. Any other problem with any listed
// trade (unknown, already settled, one-sided, stale rate, single-org) refuses
// the whole call, as in NetSettle.
//
// The resolver never scans the ledger for trades: a write transaction cannot
// page a scan on Drunix, so the candidates are exactly the listed IDs.
//
// Determinism: trades are handled in trade-ID order, banks and currencies in
// the fixed allBanks/allCurrencies order. No result depends on Go map
// iteration order.

// LiquidityRequest is the argument for PreviewLiquidity and LiquiditySettle.
type LiquidityRequest struct {
	BatchID  string   `json:"batchId"`
	TradeIDs []string `json:"tradeIds"`
}

// Removal is one step of gridlock resolution: the trade removed, and the
// shortfall that caused it.
type Removal struct {
	Step      int    `json:"step"`
	TradeID   string `json:"tradeId"`
	Bank      string `json:"bank"`      // the short bank
	Currency  string `json:"currency"`  // the currency it was short in
	Shortfall int64  `json:"shortfall"` // net outflow minus balance, before this removal
	Amount    int64  `json:"amount"`    // the removed trade's leg that bank would have paid
}

// LiquidityPlan is the resolver's result. When Gridlocked, nothing can
// settle: SettledTradeIDs is empty and NetPlan is nil.
type LiquidityPlan struct {
	BatchID         string    `json:"batchId"`
	InputTradeIDs   []string  `json:"inputTradeIds"`
	SettledTradeIDs []string  `json:"settledTradeIds"`
	DroppedTradeIDs []string  `json:"droppedTradeIds"`
	Removals        []Removal `json:"removals"`
	Cycles          []Cycle   `json:"cycles"`
	NetPlan         *NetPlan  `json:"netPlan,omitempty"`
	Gridlocked      bool      `json:"gridlocked"`
}

// LiquidityDetail is what a liquidity batch record keeps beyond the net plan.
type LiquidityDetail struct {
	InputTradeIDs   []string  `json:"inputTradeIds"`
	DroppedTradeIDs []string  `json:"droppedTradeIds"`
	Removals        []Removal `json:"removals"`
	Cycles          []Cycle   `json:"cycles"`
}

type shortfall struct {
	bank, ccy string
	amount    int64
}

// netShortfalls lists every account whose net outflow under `net` exceeds
// its balance in `before`, in allBanks then allCurrencies order.
func netShortfalls(net *NetPlan, before Balances) ([]shortfall, error) {
	out := Balances{}
	for _, n := range net.Net {
		if n.Amount <= 0 {
			continue
		}
		v, err := addChecked(out.get(n.From, n.Currency), n.Amount)
		if err != nil {
			return nil, err
		}
		out.set(n.From, n.Currency, v)
	}
	var short []shortfall
	for _, b := range allBanks {
		for _, c := range allCurrencies {
			if need, have := out.get(b, c), before.get(b, c); need > have {
				short = append(short, shortfall{bank: b, ccy: c, amount: need - have})
			}
		}
	}
	return short, nil
}

// largerShortfall reports whether shortfall a is worth strictly more than b.
// Shortfalls in different currencies are compared by value, not by raw minor
// units (a paise is worth far less than a cent): INR is converted to USD at
// the average rate of the trades being netted, gross USD / gross INR. The
// comparison cross-multiplies in big integers, so it is exact and cannot
// overflow: a(USD) x grossINR against b(INR) x grossUSD, and so on.
func largerShortfall(a, b shortfall, gross map[string]int64) bool {
	return shortfallWeight(a, gross).Cmp(shortfallWeight(b, gross)) > 0
}

func shortfallWeight(s shortfall, gross map[string]int64) *big.Int {
	v := big.NewInt(s.amount)
	if s.ccy == USD {
		return v.Mul(v, big.NewInt(gross[INR]))
	}
	return v.Mul(v, big.NewInt(gross[USD]))
}

// payerLeg is what `bank` pays in `ccy` under trade t (0 if it pays nothing).
func payerLeg(t *Trade, bank, ccy string) int64 {
	switch {
	case ccy == USD && t.USDDeliverer == bank:
		return t.USDAmount
	case ccy == INR && t.INRDeliverer == bank:
		return t.INRAmount
	}
	return 0
}

// pickRemoval applies the removal rule for shortfall s to trades, which must
// be in trade-ID order. The candidates are the trades in which s.bank pays
// s.ccy. If any candidate's leg covers the whole shortfall, the smallest such
// trade is removed: it clears the shortfall while giving up the least value.
// Otherwise the largest candidate is removed, which shrinks the shortfall the
// most. Ties go to the lower trade ID. Returns -1 if there is no candidate.
func pickRemoval(trades []*Trade, s shortfall) (int, int64) {
	best, bestAmt, bestCovers := -1, int64(0), false
	for i, t := range trades {
		amt := payerLeg(t, s.bank, s.ccy)
		if amt <= 0 {
			continue
		}
		covers := amt >= s.amount
		better := false
		switch {
		case best < 0:
			better = true
		case covers != bestCovers:
			better = covers
		case covers:
			better = amt < bestAmt
		default:
			better = amt > bestAmt
		}
		if better {
			best, bestAmt, bestCovers = i, amt, covers
		}
	}
	return best, bestAmt
}

func tradeIDs(ts []*Trade) []string {
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.TradeID
	}
	return ids
}

// resolveGridlock finds the trades of a batch that can settle together, net,
// from the balances in `before`. It is pure: PreviewLiquidity and
// LiquiditySettle run exactly the same arithmetic.
//
// Each pass nets the remaining trades. If every bank can fund its net
// outflow, those trades are the result. Otherwise the largest shortfall by
// value is taken (largerShortfall; ties go to the earlier bank in allBanks,
// then the earlier currency in allCurrencies) and one trade is removed by
// pickRemoval. A pass either
// finishes or removes a trade, so there are at most len(trades) removals. If
// every trade is removed, the batch is gridlocked: the plan reports the
// removals and an ERR_GRIDLOCK error is returned.
//
// The rule is greedy. It is deterministic and bounded, but it does not
// guarantee the largest possible settled value.
func resolveGridlock(batchID string, trades []*Trade, before Balances) (*LiquidityPlan, Balances, error) {
	current := append([]*Trade(nil), trades...)
	sort.Slice(current, func(i, j int) bool { return current[i].TradeID < current[j].TradeID })
	for i := 1; i < len(current); i++ {
		if current[i].TradeID == current[i-1].TradeID {
			return nil, nil, reject(ErrBatch, "batch %s: trade %s appears twice", batchID, current[i].TradeID)
		}
	}
	plan := &LiquidityPlan{
		BatchID:         batchID,
		InputTradeIDs:   tradeIDs(current),
		SettledTradeIDs: []string{},
		DroppedTradeIDs: []string{},
		Removals:        []Removal{},
		Cycles:          []Cycle{},
	}

	for len(current) > 0 {
		net, err := computeNet(batchID, current)
		if err != nil {
			return nil, nil, err
		}
		short, err := netShortfalls(net, before)
		if err != nil {
			return nil, nil, err
		}
		if len(short) == 0 {
			post, err := applyLegs(before, netLegs(net), "liquidity batch "+batchID)
			if err != nil {
				return nil, nil, err // unreachable: funds were just checked; fail closed anyway
			}
			plan.SettledTradeIDs = tradeIDs(current)
			plan.NetPlan = net
			if cycles := DetectCycles(current); cycles != nil {
				plan.Cycles = cycles
			}
			sort.Strings(plan.DroppedTradeIDs)
			return plan, post, nil
		}
		worst := short[0]
		for _, s := range short[1:] {
			if largerShortfall(s, worst, net.Gross) {
				worst = s
			}
		}
		i, amt := pickRemoval(current, worst)
		if i < 0 {
			return nil, nil, reject(ErrInternal, "batch %s: %s is short %d %s but pays it in no remaining trade",
				batchID, worst.bank, worst.amount, worst.ccy)
		}
		plan.Removals = append(plan.Removals, Removal{
			Step: len(plan.Removals) + 1, TradeID: current[i].TradeID,
			Bank: worst.bank, Currency: worst.ccy, Shortfall: worst.amount, Amount: amt,
		})
		plan.DroppedTradeIDs = append(plan.DroppedTradeIDs, current[i].TradeID)
		current = append(current[:i:i], current[i+1:]...)
	}

	plan.Gridlocked = true
	sort.Strings(plan.DroppedTradeIDs)
	steps := make([]string, len(plan.Removals))
	for k, r := range plan.Removals {
		steps[k] = fmt.Sprintf("%d) %s (%s short %d %s)", r.Step, r.TradeID, r.Bank, r.Shortfall, r.Currency)
	}
	return plan, nil, reject(ErrGridlock,
		"batch %s is gridlocked: none of its %d trades can be funded together; nothing settled. Removals: %s",
		batchID, len(plan.InputTradeIDs), strings.Join(steps, "; "))
}

// loadLiquidityBatch validates a LiquidityRequest and loads its trades in
// trade-ID order. Every trade must exist, be MATCHED, span two orgs and still
// be priced at a usable attested rate; otherwise the whole request is refused.
func loadLiquidityBatch(stub shim.ChaincodeStubInterface, cfg *Config, raw string) (*LiquidityRequest, []*Trade, []string, error) {
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
				"batch %s: trade %s was already settled in tx %s; the batch is refused", req.BatchID, id, t.SettledTx)
		case StatusPendingMatch:
			return nil, nil, nil, reject(ErrUnilateral,
				"batch %s: trade %s has only %s's instruction; the batch is refused", req.BatchID, id, strings.Join(t.InstructedBy, ","))
		case StatusMatched:
		default:
			return nil, nil, nil, reject(ErrInternal, "trade %s has unknown status %q", id, t.Status)
		}
		if err := requireTwoOrgs(cfg, id, t.USDDeliverer, t.INRDeliverer); err != nil {
			return nil, nil, nil, err
		}
		if _, err := checkRateUsable(stub, cfg, t.RateSeq, t.USDAmount, t.INRAmount); err != nil {
			return nil, nil, nil, err
		}
		trades = append(trades, t)
		keys = append(keys, k)
	}
	return &req, trades, keys, nil
}

// PreviewLiquidity runs the same validation and resolver as LiquiditySettle,
// read-only, against current balances. A gridlocked batch is reported with
// fundsOk=false and the removals tried, not as an error.
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
	plan, _, err := resolveGridlock(req.BatchID, trades, before)
	fundsOK, fundsMsg := true, ""
	if err != nil {
		if CodeOf(err) != ErrGridlock {
			return "", err
		}
		fundsOK, fundsMsg = false, err.Error()
	}
	return toJSON(map[string]any{"plan": plan, "fundsOk": fundsOK, "fundsMessage": fundsMsg})
}

// LiquiditySettle resolves gridlock in a batch and settles every trade the
// resolver keeps, net, in this one transaction. Dropped trades are not
// written. If the batch is gridlocked or anything fails, nothing is written.
func (c *PvPContract) LiquiditySettle(ctx contractapi.TransactionContextInterface, requestJSON string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	_, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return err
	}
	req, trades, tradeKeys, err := loadLiquidityBatch(stub, cfg, requestJSON)
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
	plan, post, err := resolveGridlock(req.BatchID, trades, before)
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
	settle := map[string]bool{}
	for _, id := range plan.SettledTradeIDs {
		settle[id] = true
	}
	txID := stub.GetTxID()
	for i, t := range trades {
		if !settle[t.TradeID] {
			continue
		}
		t.Status = StatusSettled
		t.SettledTx = txID
		t.SettledVia = "LIQUIDITY:" + req.BatchID
		if err := putJSON(stub, tradeKeys[i], t); err != nil {
			return err
		}
	}
	after := before.clone()
	for _, bank := range sortedKeys(post) {
		for _, ccy := range sortedKeys(post[bank]) {
			after.set(bank, ccy, post[bank][ccy])
		}
	}
	batch := Batch{
		NetPlan: *plan.NetPlan, SettledTx: txID, SubmittedBy: msp,
		BalancesBefore: snapshot(before), BalancesAfter: snapshot(after),
		Liquidity: &LiquidityDetail{
			InputTradeIDs: plan.InputTradeIDs, DroppedTradeIDs: plan.DroppedTradeIDs,
			Removals: plan.Removals, Cycles: plan.Cycles,
		},
	}
	if err := putJSON(stub, bk, batch); err != nil {
		return err
	}
	if err := setEvent(stub, "LiquiditySettled", map[string]any{
		"batchId": req.BatchID, "txId": txID,
		"settled": len(plan.SettledTradeIDs), "dropped": len(plan.DroppedTradeIDs),
	}); err != nil {
		return err
	}
	return appendLog(stub, "LIQUIDITY_SETTLED", msp, req.BatchID,
		fmt.Sprintf("%d of %d trades settled atomically, net (gross %d INR paise, %d USD cents); settled %s; dropped %s",
			len(plan.SettledTradeIDs), len(plan.InputTradeIDs), plan.NetPlan.Gross[INR], plan.NetPlan.Gross[USD],
			strings.Join(plan.SettledTradeIDs, ","), orNone(plan.DroppedTradeIDs)))
}

func orNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}
