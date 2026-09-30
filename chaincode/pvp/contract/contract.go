package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// PvPContract settles the two currency legs of a USD/INR trade between two
// banks so that neither leg completes unless both do.
//
// Atomicity comes from Fabric: each function below is one transaction, and
// Fabric commits all of its writes or none of them. Every function validates
// everything first and writes only at the end, so a rejected call has an
// empty write set. The endorsement policy AND(BankIN, BankFX), set at deploy
// time, stops one bank's peer from producing a valid transaction alone.
type PvPContract struct {
	contractapi.Contract
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validID(field, id string) error {
	if !idPattern.MatchString(id) {
		return reject(ErrInvalidInput, "%s %q must be 1-64 chars of [A-Za-z0-9._-]", field, id)
	}
	return nil
}

// decodeStrict rejects unknown fields and trailing data, so a caller cannot
// slip in fields the chaincode would silently ignore.
func decodeStrict(what, s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return reject(ErrInvalidInput, "%s is not valid JSON for this call: %v", what, err)
	}
	if dec.More() {
		return reject(ErrInvalidInput, "%s has trailing data", what)
	}
	return nil
}

func fmtRate(rateMicros int64) string {
	return fmt.Sprintf("%d.%06d", rateMicros/RateScale, rateMicros%RateScale)
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// InitLedger pins the configuration (bank MSPs, oracle key, rate window) and
// issues the opening simulated balances. It runs exactly once. The total of
// the opening balances becomes each currency's fixed supply, which the
// value-conservation invariant checks against for the life of the ledger.
func (c *PvPContract) InitLedger(ctx contractapi.TransactionContextInterface, requestJSON string) error {
	stub := ctx.GetStub()
	cfgKey, err := key(stub, keyConfig)
	if err != nil {
		return err
	}
	existing, err := stub.GetState(cfgKey)
	if err != nil {
		return reject(ErrInternal, "read config: %v", err)
	}
	if existing != nil {
		return reject(ErrAlreadyInitialized, "ledger is already initialised; configuration and supply cannot be replaced")
	}

	var req InitRequest
	if err := decodeStrict("init request", requestJSON, &req); err != nil {
		return err
	}
	cfg := req.Config

	// Banks: exactly BANKIN and BANKFX, distinct non-empty MSP IDs.
	if len(cfg.Banks) != 2 || cfg.Banks[BankIN] == "" || cfg.Banks[BankFX] == "" {
		return reject(ErrInvalidInput, "banks must map exactly %s and %s to MSP IDs", BankIN, BankFX)
	}
	if cfg.Banks[BankIN] == cfg.Banks[BankFX] {
		return reject(ErrInvalidInput, "%s and %s must be different MSPs", BankIN, BankFX)
	}
	for _, a := range cfg.AuditorMSPs {
		if a == "" {
			return reject(ErrInvalidInput, "auditor MSP IDs must be non-empty")
		}
		if a == cfg.Banks[BankIN] || a == cfg.Banks[BankFX] {
			return reject(ErrInvalidInput, "auditor MSP %q must not be a bank MSP", a)
		}
	}
	if o := cfg.OracleMSP; o != "" {
		if o == cfg.Banks[BankIN] || o == cfg.Banks[BankFX] || contains(cfg.AuditorMSPs, o) {
			return reject(ErrInvalidInput, "oracle MSP %q must not be a bank or auditor MSP", o)
		}
	}
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != cfg.Banks[BankIN] && msp != cfg.Banks[BankFX] {
		return reject(ErrUnauthorized, "only a settlement bank may initialise the ledger (submitter %q)", msp)
	}
	if cfg.Pair != "USD/INR" {
		return reject(ErrInvalidInput, "only the USD/INR pair is supported, got %q", cfg.Pair)
	}
	pub, err := base64.StdEncoding.DecodeString(cfg.OraclePublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return reject(ErrInvalidInput, "oraclePublicKey must be a base64 Ed25519 public key (32 bytes)")
	}
	if err := safeField("oracleName", cfg.OracleName, true); err != nil {
		return err
	}
	if cfg.RateWindow < 1 || cfg.RateWindow > 100 {
		return reject(ErrInvalidInput, "rateWindow must be 1..100, got %d", cfg.RateWindow)
	}

	// Opening balances: both banks, both currencies, non-negative integers.
	if len(req.Balances) != 2 {
		return reject(ErrInvalidInput, "balances must cover exactly %s and %s", BankIN, BankFX)
	}
	opening := Balances{}
	supply := map[string]int64{}
	for _, bank := range allBanks {
		m, ok := req.Balances[bank]
		if !ok || len(m) != len(allCurrencies) {
			return reject(ErrInvalidInput, "balances for %s must cover exactly INR and USD", bank)
		}
		for _, ccy := range allCurrencies {
			s, ok := m[ccy]
			if !ok {
				return reject(ErrInvalidInput, "missing %s opening balance for %s", ccy, bank)
			}
			v := int64(0)
			if s != "0" {
				if v, err = ParseAmount(fmt.Sprintf("opening %s/%s", bank, ccy), s); err != nil {
					return err
				}
			}
			opening.set(bank, ccy, v)
			if supply[ccy], err = addChecked(supply[ccy], v); err != nil {
				return err
			}
		}
	}

	// Refuse to initialise over stray balances written by any other means.
	if _, n, err := readAllBalances(stub); err != nil {
		return err
	} else if n != 0 {
		return reject(ErrInvariantViolation, "found %d balance(s) on the ledger before initialisation", n)
	}

	// Validation complete. Writes start here.
	if err := putJSON(stub, cfgKey, cfg); err != nil {
		return err
	}
	if err := writeBalances(stub, opening); err != nil {
		return err
	}
	for _, ccy := range allCurrencies {
		k, err := key(stub, keySupply, ccy)
		if err != nil {
			return err
		}
		if err := putJSON(stub, k, supply[ccy]); err != nil {
			return err
		}
	}
	return appendLog(stub, "INIT", msp, "config",
		fmt.Sprintf("banks %s=%s %s=%s; oracle %q (MSP %q); auditors %v; supply INR=%d USD=%d",
			BankIN, cfg.Banks[BankIN], BankFX, cfg.Banks[BankFX], cfg.OracleName, cfg.OracleMSP, cfg.AuditorMSPs, supply[INR], supply[USD]))
}

// ---------------------------------------------------------------------------
// FX oracle
// ---------------------------------------------------------------------------

func rateHead(stub shim.ChaincodeStubInterface) (int64, error) {
	k, err := key(stub, keyRateHead)
	if err != nil {
		return 0, err
	}
	var head int64
	if _, err := getJSON(stub, k, &head); err != nil {
		return 0, err
	}
	return head, nil
}

func loadRate(stub shim.ChaincodeStubInterface, seq int64) (*RateRecord, error) {
	k, err := key(stub, keyRate, seqAttr(seq))
	if err != nil {
		return nil, err
	}
	var r RateRecord
	found, err := getJSON(stub, k, &r)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, reject(ErrAttestationUnknown, "no oracle-attested rate with seq %d has been published", seq)
	}
	return &r, nil
}

// checkRateUsable enforces freshness (the rate's seq must be within
// RateWindow of the latest published seq; seq gaps are allowed, so fewer than
// RateWindow rates may be usable) and that the INR leg is exactly the USD leg at the
// attested rate. Freshness is by oracle sequence, not by clock: chaincode
// must be deterministic, and a submitter can choose its own tx timestamp.
func checkRateUsable(stub shim.ChaincodeStubInterface, cfg *Config, seq, usd, inr int64) (*RateRecord, error) {
	if seq < 1 {
		return nil, reject(ErrAttestationUnknown, "rateSeq must reference a published oracle rate, got %d", seq)
	}
	r, err := loadRate(stub, seq)
	if err != nil {
		return nil, err
	}
	head, err := rateHead(stub)
	if err != nil {
		return nil, err
	}
	if seq <= head-int64(cfg.RateWindow) {
		return nil, reject(ErrAttestationStale,
			"rate seq %d is stale: the latest published seq is %d and a rate is usable only if its seq is within %d of it (seq gaps are allowed)",
			seq, head, cfg.RateWindow)
	}
	expected, err := ConvertUSDToINR(usd, r.RateMicros)
	if err != nil {
		return nil, err
	}
	if inr != expected {
		return nil, reject(ErrRateMismatch,
			"INR leg %d paise is not the USD leg %d cents at the attested rate %s (seq %d, %s); expected %d paise. Out-of-band rates are refused",
			inr, usd, fmtRate(r.RateMicros), seq, r.Source, expected)
	}
	return r, nil
}

// PublishRate records an Oracle-signed FX rate. If an oracle MSP is pinned,
// only that org may submit it; auditors never may. The chaincode accepts the
// rate only if the signature verifies against the oracle key pinned at
// InitLedger and its sequence is newer than every rate already published.
func (c *PvPContract) PublishRate(ctx contractapi.TransactionContextInterface, attestationJSON string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if contains(cfg.AuditorMSPs, msp) {
		return reject(ErrUnauthorized, "submitter MSP %q is an auditor; auditors have read-only access", msp)
	}
	if cfg.OracleMSP != "" && msp != cfg.OracleMSP {
		return reject(ErrUnauthorized, "only the oracle org %q may publish rates (submitter %q)", cfg.OracleMSP, msp)
	}
	var a Attestation
	if err := decodeStrict("attestation", attestationJSON, &a); err != nil {
		return err
	}
	if err := verifyAttestation(cfg, a); err != nil {
		return err
	}
	head, err := rateHead(stub)
	if err != nil {
		return err
	}
	if a.Seq <= head {
		return reject(ErrAttestationStale,
			"rate seq %d is not newer than the latest published seq %d; old or replayed rates are refused", a.Seq, head)
	}

	rec := RateRecord{Attestation: a, PublishedTx: stub.GetTxID(), PublishedBy: msp, VerifiedWith: cfg.OraclePublicKey}
	k, err := key(stub, keyRate, seqAttr(a.Seq))
	if err != nil {
		return err
	}
	if err := putJSON(stub, k, rec); err != nil {
		return err
	}
	hk, err := key(stub, keyRateHead)
	if err != nil {
		return err
	}
	if err := putJSON(stub, hk, a.Seq); err != nil {
		return err
	}
	return appendLog(stub, "RATE_PUBLISHED", msp, strconv.FormatInt(a.Seq, 10),
		fmt.Sprintf("%s = %s from %q (as of %s), oracle signature verified", a.Pair, fmtRate(a.RateMicros), a.Source, a.AsOf))
}

// ---------------------------------------------------------------------------
// Trades
// ---------------------------------------------------------------------------

func loadTrade(stub shim.ChaincodeStubInterface, id string) (*Trade, string, error) {
	k, err := key(stub, keyTrade, id)
	if err != nil {
		return nil, "", err
	}
	var t Trade
	found, err := getJSON(stub, k, &t)
	if err != nil {
		return nil, k, err
	}
	if !found {
		return nil, k, nil
	}
	return &t, k, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// SubmitInstruction records one bank's commitment to a trade's exact terms.
// A trade can settle only after BOTH banks have instructed identical terms.
// The instructing bank is taken from the submitter's authenticated MSP;
// a bank cannot instruct on the other bank's behalf.
func (c *PvPContract) SubmitInstruction(ctx contractapi.TransactionContextInterface, instructionJSON string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	bank, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return err
	}
	var in Instruction
	if err := decodeStrict("instruction", instructionJSON, &in); err != nil {
		return err
	}
	if err := validID("tradeId", in.TradeID); err != nil {
		return err
	}
	if !isBank(in.AsBank) {
		return reject(ErrInvalidInput, "asBank must be %s or %s, got %q", BankIN, BankFX, in.AsBank)
	}
	if in.AsBank != bank {
		return reject(ErrForgedInstruction,
			"submitter is %s (%s) but the instruction claims to come from %s; a bank cannot instruct for its counterparty",
			msp, bank, in.AsBank)
	}
	if !isBank(in.USDDeliverer) {
		return reject(ErrInvalidInput, "usdDeliverer must be %s or %s, got %q", BankIN, BankFX, in.USDDeliverer)
	}
	usd, err := ParseAmount("usdAmount", in.USDAmount)
	if err != nil {
		return err
	}
	inr, err := ParseAmount("inrAmount", in.INRAmount)
	if err != nil {
		return err
	}

	trade, tradeKey, err := loadTrade(stub, in.TradeID)
	if err != nil {
		return err
	}
	if trade != nil && trade.Status == StatusSettled {
		return reject(ErrReplay,
			"trade %s already settled in tx %s; replaying an instruction cannot reopen or re-run it", in.TradeID, trade.SettledTx)
	}
	if trade != nil && contains(trade.InstructedBy, bank) {
		return reject(ErrDuplicateInstruction, "%s has already instructed trade %s", bank, in.TradeID)
	}

	rate, err := checkRateUsable(stub, cfg, in.RateSeq, usd, inr)
	if err != nil {
		return err
	}

	if trade == nil {
		trade = &Trade{
			TradeID: in.TradeID, Status: StatusPendingMatch,
			USDDeliverer: in.USDDeliverer, INRDeliverer: otherBank(in.USDDeliverer),
			USDAmount: usd, INRAmount: inr, RateSeq: in.RateSeq, RateMicros: rate.RateMicros,
			InstructedBy: []string{bank},
		}
	} else {
		var diffs []string
		if trade.USDDeliverer != in.USDDeliverer {
			diffs = append(diffs, fmt.Sprintf("usdDeliverer %s vs %s", trade.USDDeliverer, in.USDDeliverer))
		}
		if trade.USDAmount != usd {
			diffs = append(diffs, fmt.Sprintf("usdAmount %d vs %d", trade.USDAmount, usd))
		}
		if trade.INRAmount != inr {
			diffs = append(diffs, fmt.Sprintf("inrAmount %d vs %d", trade.INRAmount, inr))
		}
		if trade.RateSeq != in.RateSeq {
			diffs = append(diffs, fmt.Sprintf("rateSeq %d vs %d", trade.RateSeq, in.RateSeq))
		}
		if len(diffs) > 0 {
			return reject(ErrInstructionMismatch,
				"%s's instruction for trade %s does not match %s's: %s",
				bank, in.TradeID, otherBank(bank), strings.Join(diffs, "; "))
		}
		trade.InstructedBy = append(trade.InstructedBy, bank)
		sort.Strings(trade.InstructedBy)
		trade.Status = StatusMatched
	}

	// Validation complete. Writes start here.
	ik, err := key(stub, keyInstr, in.TradeID, bank)
	if err != nil {
		return err
	}
	if err := putJSON(stub, ik, StoredInstruction{Instruction: in, SubmitterMSP: msp, TxID: stub.GetTxID()}); err != nil {
		return err
	}
	if err := putJSON(stub, tradeKey, trade); err != nil {
		return err
	}
	return appendLog(stub, "INSTRUCTED", msp, in.TradeID,
		fmt.Sprintf("%s instructed: %s pays %d USD cents, %s pays %d INR paise at rate seq %d (%s); trade now %s",
			bank, trade.USDDeliverer, usd, trade.INRDeliverer, inr, in.RateSeq, fmtRate(rate.RateMicros), trade.Status))
}

// SettleTrade atomically settles both legs of one matched trade:
// the USD deliverer pays USD and the INR deliverer pays INR, in the same
// transaction. If either leg cannot be paid, nothing is written.
func (c *PvPContract) SettleTrade(ctx contractapi.TransactionContextInterface, tradeID string) error {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return err
	}
	_, msp, err := callerBank(ctx, cfg)
	if err != nil {
		return err
	}
	if err := validID("tradeId", tradeID); err != nil {
		return err
	}
	trade, tradeKey, err := loadTrade(stub, tradeID)
	if err != nil {
		return err
	}
	if trade == nil {
		return reject(ErrTradeNotFound, "no trade %s has been instructed", tradeID)
	}
	switch trade.Status {
	case StatusSettled:
		return reject(ErrAlreadySettled, "trade %s was already settled in tx %s; it cannot settle twice", tradeID, trade.SettledTx)
	case StatusPendingMatch:
		return reject(ErrUnilateral,
			"trade %s has only %s's instruction; settlement needs matching instructions from both banks",
			tradeID, strings.Join(trade.InstructedBy, ","))
	case StatusMatched:
	default:
		return reject(ErrInternal, "trade %s has unknown status %q", tradeID, trade.Status)
	}
	// Re-check the rate at settlement time: it may have gone stale since matching.
	if _, err := checkRateUsable(stub, cfg, trade.RateSeq, trade.USDAmount, trade.INRAmount); err != nil {
		return err
	}

	before, _, err := readAllBalances(stub)
	if err != nil {
		return err
	}
	post, err := applyLegs(before, []leg{
		{From: trade.USDDeliverer, To: trade.INRDeliverer, Ccy: USD, Amount: trade.USDAmount},
		{From: trade.INRDeliverer, To: trade.USDDeliverer, Ccy: INR, Amount: trade.INRAmount},
	}, "trade "+tradeID)
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
	trade.Status = StatusSettled
	trade.SettledTx = stub.GetTxID()
	trade.SettledVia = "GROSS"
	trade.BalancesBefore = snapshot(before)
	trade.BalancesAfter = snapshot(post)
	if err := putJSON(stub, tradeKey, trade); err != nil {
		return err
	}
	if err := setEvent(stub, "Settled", map[string]any{"tradeId": tradeID, "txId": stub.GetTxID()}); err != nil {
		return err
	}
	return appendLog(stub, "SETTLED", msp, tradeID,
		fmt.Sprintf("both legs settled atomically: %s paid %d USD cents to %s, %s paid %d INR paise to %s",
			trade.USDDeliverer, trade.USDAmount, trade.INRDeliverer, trade.INRDeliverer, trade.INRAmount, trade.USDDeliverer))
}

// leg is one directed movement of one currency.
type leg struct {
	From, To, Ccy string
	Amount        int64
}

// applyLegs computes the post-state of moving every leg, in memory, starting
// from `before`. It returns only the accounts it changed. If any payer cannot
// cover its total outflow, it rejects and reports every shortfall.
func applyLegs(before Balances, legs []leg, what string) (Balances, error) {
	post := Balances{}
	cur := func(bank, ccy string) int64 {
		if m, ok := post[bank]; ok {
			if v, ok := m[ccy]; ok {
				return v
			}
		}
		return before.get(bank, ccy)
	}
	// Funds check on total outflow per account, before moving anything.
	need := Balances{}
	for _, l := range legs {
		if l.Amount <= 0 || l.From == l.To || !isBank(l.From) || !isBank(l.To) {
			return nil, reject(ErrInternal, "invalid leg %+v", l)
		}
		n, err := addChecked(need.get(l.From, l.Ccy), l.Amount)
		if err != nil {
			return nil, err
		}
		need.set(l.From, l.Ccy, n)
	}
	var short []string
	for _, bank := range sortedKeys(need) {
		for _, ccy := range sortedKeys(need[bank]) {
			if have := before.get(bank, ccy); have < need[bank][ccy] {
				short = append(short, fmt.Sprintf("%s holds %d %s but must pay %d", bank, have, ccy, need[bank][ccy]))
			}
		}
	}
	if len(short) > 0 {
		return nil, reject(ErrInsufficientFunds, "%s cannot settle: %s. Neither leg was paid",
			what, strings.Join(short, "; "))
	}
	for _, l := range legs {
		from, err := addChecked(cur(l.From, l.Ccy), -l.Amount)
		if err != nil {
			return nil, err
		}
		to, err := addChecked(cur(l.To, l.Ccy), l.Amount)
		if err != nil {
			return nil, err
		}
		post.set(l.From, l.Ccy, from)
		post.set(l.To, l.Ccy, to)
	}
	return post, nil
}

// snapshot returns the four bank balances with post-state overrides.
func snapshot(b Balances) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, bank := range allBanks {
		out[bank] = map[string]int64{}
		for _, ccy := range allCurrencies {
			out[bank][ccy] = b.get(bank, ccy)
		}
	}
	return out
}

func setEvent(stub shim.ChaincodeStubInterface, name string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return reject(ErrInternal, "encode event: %v", err)
	}
	if err := stub.SetEvent(name, raw); err != nil {
		return reject(ErrInternal, "set event: %v", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Queries (read-only; never write)
// ---------------------------------------------------------------------------

func toJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", reject(ErrInternal, "encode: %v", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// GetConfig returns the pinned configuration.
func (c *PvPContract) GetConfig(ctx contractapi.TransactionContextInterface) (string, error) {
	cfg, err := loadConfig(ctx.GetStub())
	if err != nil {
		return "", err
	}
	return toJSON(cfg)
}

// GetBalances returns every balance on the ledger and the live
// value-conservation check computed from those same balances.
func (c *PvPContract) GetBalances(ctx contractapi.TransactionContextInterface) (string, error) {
	stub := ctx.GetStub()
	if _, err := loadConfig(stub); err != nil {
		return "", err
	}
	all, _, err := readAllBalances(stub)
	if err != nil {
		return "", err
	}
	reports, err := conservationReport(stub, all)
	if err != nil {
		return "", err
	}
	return toJSON(map[string]any{"balances": snapshot(all), "conservation": reports})
}

// CheckInvariant recomputes the value-conservation invariant from the real
// balances on the ledger.
func (c *PvPContract) CheckInvariant(ctx contractapi.TransactionContextInterface) (string, error) {
	stub := ctx.GetStub()
	if _, err := loadConfig(stub); err != nil {
		return "", err
	}
	all, _, err := readAllBalances(stub)
	if err != nil {
		return "", err
	}
	reports, err := conservationReport(stub, all)
	if err != nil {
		return "", err
	}
	holds := true
	for _, r := range reports {
		holds = holds && r.Holds
	}
	return toJSON(map[string]any{"holds": holds, "currencies": reports})
}

// GetRates returns every published rate, oldest first, plus the head seq.
func (c *PvPContract) GetRates(ctx contractapi.TransactionContextInterface) (string, error) {
	stub := ctx.GetStub()
	cfg, err := loadConfig(stub)
	if err != nil {
		return "", err
	}
	head, err := rateHead(stub)
	if err != nil {
		return "", err
	}
	rates := []RateRecord{}
	if err := scan(stub, keyRate, func(raw []byte) error {
		var r RateRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		rates = append(rates, r)
		return nil
	}); err != nil {
		return "", err
	}
	return toJSON(map[string]any{"head": head, "rateWindow": cfg.RateWindow, "rates": rates})
}

// QuoteINR returns the INR leg (paise) that the chaincode will accept for a
// USD leg (cents) at a published rate, so clients never compute it themselves.
func (c *PvPContract) QuoteINR(ctx contractapi.TransactionContextInterface, usdAmount string, rateSeq string) (string, error) {
	stub := ctx.GetStub()
	if _, err := loadConfig(stub); err != nil {
		return "", err
	}
	usd, err := ParseAmount("usdAmount", usdAmount)
	if err != nil {
		return "", err
	}
	seq, err := strconv.ParseInt(rateSeq, 10, 64)
	if err != nil {
		return "", reject(ErrInvalidInput, "rateSeq %q is not an integer", rateSeq)
	}
	r, err := loadRate(stub, seq)
	if err != nil {
		return "", err
	}
	inr, err := ConvertUSDToINR(usd, r.RateMicros)
	if err != nil {
		return "", err
	}
	return toJSON(map[string]any{"usdAmount": usd, "rateSeq": seq, "rateMicros": r.RateMicros, "inrAmount": inr})
}

// GetTrade returns one trade and both banks' stored instructions.
func (c *PvPContract) GetTrade(ctx contractapi.TransactionContextInterface, tradeID string) (string, error) {
	stub := ctx.GetStub()
	if err := validID("tradeId", tradeID); err != nil {
		return "", err
	}
	t, _, err := loadTrade(stub, tradeID)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", reject(ErrTradeNotFound, "no trade %s", tradeID)
	}
	instr := map[string]StoredInstruction{}
	for _, b := range allBanks {
		k, err := key(stub, keyInstr, tradeID, b)
		if err != nil {
			return "", err
		}
		var si StoredInstruction
		found, err := getJSON(stub, k, &si)
		if err != nil {
			return "", err
		}
		if found {
			instr[b] = si
		}
	}
	return toJSON(map[string]any{"trade": t, "instructions": instr})
}

// GetTrades returns every trade, ordered by trade ID.
func (c *PvPContract) GetTrades(ctx contractapi.TransactionContextInterface) (string, error) {
	trades := []Trade{}
	if err := scan(ctx.GetStub(), keyTrade, func(raw []byte) error {
		var t Trade
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		trades = append(trades, t)
		return nil
	}); err != nil {
		return "", err
	}
	return toJSON(trades)
}

// GetAuditLog returns the append-only audit log, oldest first.
//
// Entries are numbered 1..LOGHEAD with no gaps (appendLog), so they are read
// by point lookups rather than a range scan. That is exact and ordered on any
// state database, including Drunix's SQL one, whose range scans are capped
// and unordered (see scan).
func (c *PvPContract) GetAuditLog(ctx contractapi.TransactionContextInterface) (string, error) {
	stub := ctx.GetStub()
	hk, err := key(stub, keyLogHead)
	if err != nil {
		return "", err
	}
	var head int64
	if _, err := getJSON(stub, hk, &head); err != nil {
		return "", err
	}
	entries := make([]LogEntry, 0, head)
	for n := int64(1); n <= head; n++ {
		lk, err := key(stub, keyLog, seqAttr(n))
		if err != nil {
			return "", err
		}
		var e LogEntry
		found, err := getJSON(stub, lk, &e)
		if err != nil {
			return "", err
		}
		if !found {
			// The log is append-only and gap-free; a hole means state is not
			// what this chaincode wrote. Fail closed rather than skip it.
			return "", reject(ErrInternal, "audit log entry %d of %d is missing", n, head)
		}
		entries = append(entries, e)
	}
	return toJSON(entries)
}

// maxListPage bounds one read-only list query (see scan).
const maxListPage = 10_000

// scan visits every value under a composite-key prefix, in key order.
//
// It uses a paginated query because on Drunix's SQL state database a plain
// range scan is a paginated scan with a fixed page size of 10
// (statesqldb.GetStateRangeScanIterator), so it silently drops everything
// after the 10th row. Drunix's bookmarks do not resume a scan (its scanner
// always restarts at offset 0), so this asks for one page of maxListPage and
// fails closed if that page is full instead of returning a partial list.
// Drunix also returns rows unordered and a row may repeat (LIMIT/OFFSET with
// no ORDER BY), so rows are de-duplicated by key and visited in key order.
//
// Fabric only allows paginated queries in read-only transactions, so scan
// must only be called from query functions, never from one that writes.
func scan(stub shim.ChaincodeStubInterface, objectType string, visit func([]byte) error) error {
	it, _, err := stub.GetStateByPartialCompositeKeyWithPagination(objectType, []string{}, maxListPage, "")
	if err != nil {
		return reject(ErrInternal, "scan %s: %v", objectType, err)
	}
	defer it.Close()
	rows := map[string][]byte{}
	fetched := 0
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return reject(ErrInternal, "scan %s: %v", objectType, err)
		}
		fetched++
		rows[kv.Key] = kv.Value
	}
	if fetched >= maxListPage {
		return reject(ErrInternal, "scan %s returned a full page of %d rows; refusing to return a list that may be incomplete", objectType, maxListPage)
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := visit(rows[k]); err != nil {
			return reject(ErrInternal, "decode %s: %v", k, err)
		}
	}
	return nil
}
