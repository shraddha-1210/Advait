package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/advait/pvp-settlement/chaincode/attest"
	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
)

// AttackInfo describes one row of the threat matrix (CLAUDE.md §6).
type AttackInfo struct {
	Name        string `json:"name"`
	Threat      string `json:"threat"` // the threat-matrix row
	Actor       string `json:"actor"`
	Description string `json:"description"`
	Expect      string `json:"expect"` // expected rejection code
	Stage       string `json:"stage"`  // where the network refuses it
}

// AttackReport is the real result of running one attack.
type AttackReport struct {
	AttackInfo
	TradeID string           `json:"tradeId,omitempty"` // trade the attack targeted, if any
	Setup   []ledger.Outcome `json:"setup"`             // legitimate transactions run first (committed)
	Attempt string           `json:"attempt"`           // what the attacker sent, in words
	Payload []string         `json:"payload"`           // the exact chaincode arguments sent
	Result  *WriteResult     `json:"result"`            // the attack transaction's real outcome
	Refused bool             `json:"refused"`
	// ExpectedCode is true when the refusal came from the defence this row
	// is about (Result.Outcome.Code == Expect).
	ExpectedCode bool   `json:"expectedCode"`
	Note         string `json:"note,omitempty"`
}

var catalogue = []AttackInfo{
	{"double-settle", "Double-settle same tradeId", "BankFX (hostile)",
		"Settle a trade that has already settled, hoping to be paid twice.", "ERR_ALREADY_SETTLED", "endorse"},
	{"replay-instruction", "Replay a prior settlement", "BankFX (hostile)",
		"Re-submit its own old instruction for a settled trade, byte for byte, to reopen and re-run it.", "ERR_REPLAY", "endorse"},
	{"underfunded", "Settle while underfunded", "Either bank",
		"Both banks agree a trade the USD payer cannot fund. The INR leg is fundable. Neither leg may move.", "ERR_INSUFFICIENT_FUNDS", "endorse"},
	{"unilateral-endorsement", "Unilateral settle (one endorsement)", "BankFX (hostile, controls its own peer)",
		"Submit a settlement endorsed by BankFX's peer only. The chaincode policy is AND(BankIN, BankFX).", "ENDORSEMENT_POLICY_FAILURE", "commit"},
	{"unilateral-instruction", "Unilateral settle (no counterparty consent)", "BankFX (hostile)",
		"Instruct a trade alone and try to settle it without BankIN ever agreeing.", "ERR_UNILATERAL", "endorse"},
	{"forged-instruction", "Malicious org forges", "BankFX (hostile)",
		"Submit an instruction that claims to come from BankIN, committing BankIN to pay.", "ERR_FORGED_INSTRUCTION", "endorse"},
	{"mismatched-instruction", "Malicious org forges", "BankFX (hostile)",
		"BankIN instructs 10,000 USD. BankFX 'matches' it with 9,000 USD, hoping the smaller figure settles.", "ERR_INSTRUCTION_MISMATCH", "endorse"},
	{"unsigned-rate", "Poisoned / unsigned / stale FX rate", "BankFX (hostile)",
		"Publish an FX rate with no oracle signature.", "ERR_ATTESTATION_UNSIGNED", "endorse"},
	{"tampered-rate", "Poisoned / unsigned / stale FX rate", "BankFX (hostile)",
		"Take a genuine oracle-signed rate and change the number, keeping the signature.", "ERR_ATTESTATION_BAD_SIGNATURE", "endorse"},
	{"fake-oracle", "Malicious org colludes with a bad oracle", "BankFX + colluding oracle",
		"Publish a rate correctly signed by a different key (an oracle the ledger never pinned).", "ERR_ATTESTATION_BAD_SIGNATURE", "endorse"},
	{"stale-rate", "Poisoned / unsigned / stale FX rate", "BankFX (hostile)",
		"Re-publish an old, genuinely oracle-signed rate to price trades at yesterday's number.", "ERR_ATTESTATION_STALE", "endorse"},
	{"out-of-band-rate", "Poisoned / unsigned / stale FX rate", "BankIN (hostile)",
		"Price the INR leg 5% away from the attested rate (an off-ledger 'side deal').", "ERR_RATE_MISMATCH", "endorse"},
	{"negative-amount", "Negative / overflow amount", "BankFX (hostile)",
		"Instruct a negative USD amount, hoping to reverse the direction of payment.", "ERR_INVALID_AMOUNT", "endorse"},
	{"overflow-amount", "Negative / overflow amount", "BankFX (hostile)",
		"Instruct 9223372036854775808 cents (one more than int64 can hold), hoping to wrap around.", "ERR_AMOUNT_OVERFLOW", "endorse"},
	{"reinit", "Value-conservation breach (create value)", "BankFX (hostile)",
		"Re-run InitLedger with its own oracle key and a huge USD balance, i.e. mint money.", "ERR_ALREADY_INITIALIZED", "endorse"},
	{"bank-offline", "Peer down (one bank offline)", "Network failure",
		"Stop BankFX's endorsing peer, then have BankIN try to settle a matched trade.", "ENDORSER_UNAVAILABLE", "endorse"},
}

func (s *Server) attackCatalogue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, catalogue)
}

func (s *Server) runAttack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var info *AttackInfo
	for i := range catalogue {
		if catalogue[i].Name == name {
			info = &catalogue[i]
		}
	}
	if info == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown attack %q", name))
		return
	}
	rep, err := s.attack(*info)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---------------------------------------------------------------------------
// setup helpers (legitimate transactions; they must succeed)

func newTradeID(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano()%1e12, 36)
}

func (s *Server) quoteINR(usdCents, seq int64) (int64, error) {
	raw, err := s.L.Evaluate(ledger.BankIN, "QuoteINR", itoa(usdCents), itoa(seq))
	if err != nil {
		return 0, err
	}
	var q struct {
		INRAmount int64 `json:"inrAmount"`
	}
	return q.INRAmount, json.Unmarshal(raw, &q)
}

func (s *Server) mustSubmit(setup *[]ledger.Outcome, p ledger.Party, fn string, args ...string) error {
	out := s.L.Submit(p, fn, args, ledger.SubmitOptions{})
	*setup = append(*setup, out)
	if !out.OK {
		return fmt.Errorf("setup step %s as %s failed: %s %s", fn, p, out.Code, out.Message)
	}
	return nil
}

// matchedTrade has both banks instruct identical terms at the latest rate.
func (s *Server) matchedTrade(setup *[]ledger.Outcome, id, usdDeliverer string, usdCents int64) error {
	seq, err := s.rateHead()
	if err != nil {
		return err
	}
	inr, err := s.quoteINR(usdCents, seq)
	if err != nil {
		return err
	}
	for _, p := range []ledger.Party{ledger.BankIN, ledger.BankFX} {
		body := InstructionBody{As: string(p), TradeID: id, USDDeliverer: usdDeliverer,
			USDAmount: itoa(usdCents), INRAmount: itoa(inr), RateSeq: seq}
		if err := s.mustSubmit(setup, p, "SubmitInstruction", instructionJSON(body)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

func (s *Server) attack(info AttackInfo) (*AttackReport, error) {
	rep := &AttackReport{AttackInfo: info, Setup: []ledger.Outcome{}}
	var (
		party = ledger.BankFX
		fn    string
		args  []string
		opt   ledger.SubmitOptions
	)
	seq, err := s.rateHead()
	if err != nil {
		return nil, err
	}

	switch info.Name {
	case "double-settle":
		id := newTradeID("ATK-DBL")
		rep.TradeID = id
		if err := s.matchedTrade(&rep.Setup, id, "BANKFX", 1_000_00); err != nil {
			return nil, err
		}
		if err := s.mustSubmit(&rep.Setup, ledger.BankIN, "SettleTrade", id); err != nil {
			return nil, err
		}
		fn, args = "SettleTrade", []string{id}
		rep.Attempt = fmt.Sprintf("BankFX calls SettleTrade(%s) again after it already settled", id)

	case "replay-instruction":
		id := newTradeID("ATK-RPL")
		rep.TradeID = id
		if err := s.matchedTrade(&rep.Setup, id, "BANKFX", 1_000_00); err != nil {
			return nil, err
		}
		if err := s.mustSubmit(&rep.Setup, ledger.BankIN, "SettleTrade", id); err != nil {
			return nil, err
		}
		raw, err := s.L.Evaluate(ledger.BankFX, "GetTrade", id)
		if err != nil {
			return nil, err
		}
		var t struct {
			Instructions map[string]map[string]any `json:"instructions"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		old := t.Instructions["BANKFX"]
		replay, _ := json.Marshal(map[string]any{
			"tradeId": old["tradeId"], "asBank": old["asBank"], "usdDeliverer": old["usdDeliverer"],
			"usdAmount": old["usdAmount"], "inrAmount": old["inrAmount"], "rateSeq": old["rateSeq"],
		})
		fn, args = "SubmitInstruction", []string{string(replay)}
		rep.Attempt = fmt.Sprintf("BankFX re-submits its original instruction for settled trade %s (first sent in tx %v)", id, old["txId"])

	case "underfunded":
		snap, err := s.snapshot()
		if err != nil {
			return nil, err
		}
		usd := snap.Balances["BANKFX"]["USD"] + 1 // one cent more than BankFX holds
		id := newTradeID("ATK-UF")
		rep.TradeID = id
		if err := s.matchedTrade(&rep.Setup, id, "BANKFX", usd); err != nil {
			return nil, err
		}
		party, fn, args = ledger.BankIN, "SettleTrade", []string{id}
		rep.Attempt = fmt.Sprintf("BankIN settles %s: BankFX must pay %d USD cents but holds %d", id, usd, usd-1)

	case "unilateral-endorsement":
		id := newTradeID("ATK-1END")
		rep.TradeID = id
		if err := s.matchedTrade(&rep.Setup, id, "BANKFX", 100_00); err != nil {
			return nil, err
		}
		fn, args = "SettleTrade", []string{id}
		opt = ledger.SubmitOptions{EndorsingOrgs: []string{s.L.MSPID(ledger.BankFX)}}
		rep.Attempt = fmt.Sprintf("BankFX submits SettleTrade(%s) endorsed only by its own org (%s)", id, s.L.MSPID(ledger.BankFX))
		rep.Note = "The trade itself is valid and fully funded. It is refused only because one org's endorsement is not enough. It stays MATCHED and can still be settled properly."

	case "unilateral-instruction":
		id := newTradeID("ATK-SOLO")
		rep.TradeID = id
		inr, err := s.quoteINR(1_000_000_00, seq)
		if err != nil {
			return nil, err
		}
		body := InstructionBody{As: "BANKFX", TradeID: id, USDDeliverer: "BANKIN", USDAmount: itoa(1_000_000_00), INRAmount: itoa(inr), RateSeq: seq}
		if err := s.mustSubmit(&rep.Setup, ledger.BankFX, "SubmitInstruction", instructionJSON(body)); err != nil {
			return nil, err
		}
		fn, args = "SettleTrade", []string{id}
		rep.Attempt = fmt.Sprintf("BankFX instructed %s alone and now tries to settle it", id)

	case "forged-instruction":
		inr, err := s.quoteINR(1_000_000_00, seq)
		if err != nil {
			return nil, err
		}
		body := InstructionBody{As: "BANKFX", AsBank: "BANKIN", TradeID: newTradeID("ATK-FORGE"), USDDeliverer: "BANKFX",
			USDAmount: itoa(1_000_000_00), INRAmount: itoa(inr), RateSeq: seq}
		fn, args = "SubmitInstruction", []string{instructionJSON(body)}
		rep.Attempt = "BankFX (Org2MSP) submits an instruction with asBank=BANKIN"

	case "mismatched-instruction":
		id := newTradeID("ATK-MIS")
		rep.TradeID = id
		inr, err := s.quoteINR(10_000_00, seq)
		if err != nil {
			return nil, err
		}
		honest := InstructionBody{As: "BANKIN", TradeID: id, USDDeliverer: "BANKFX", USDAmount: itoa(10_000_00), INRAmount: itoa(inr), RateSeq: seq}
		if err := s.mustSubmit(&rep.Setup, ledger.BankIN, "SubmitInstruction", instructionJSON(honest)); err != nil {
			return nil, err
		}
		inr9, err := s.quoteINR(9_000_00, seq)
		if err != nil {
			return nil, err
		}
		lie := InstructionBody{As: "BANKFX", TradeID: id, USDDeliverer: "BANKFX", USDAmount: itoa(9_000_00), INRAmount: itoa(inr9), RateSeq: seq}
		fn, args = "SubmitInstruction", []string{instructionJSON(lie)}
		rep.Attempt = fmt.Sprintf("BankIN instructed 10,000.00 USD on %s; BankFX instructs 9,000.00 USD", id)

	case "unsigned-rate":
		a := s.Oracle.Sign(seq+1, 70_000_000, time.Now())
		a.Signature = ""
		raw, _ := json.Marshal(a)
		fn, args = "PublishRate", []string{string(raw)}
		rep.Attempt = fmt.Sprintf("BankFX publishes rate seq %d = 70.000000 with no signature", seq+1)

	case "tampered-rate":
		a := s.Oracle.Sign(seq+1, 83_250_000, time.Now())
		a.RateMicros = 70_000_000
		raw, _ := json.Marshal(a)
		fn, args = "PublishRate", []string{string(raw)}
		rep.Attempt = fmt.Sprintf("BankFX takes the oracle's signed seq %d rate of 83.250000 and edits it to 70.000000", seq+1)

	case "fake-oracle":
		rogue := oracle.New()
		a := rogue.Sign(seq+1, 70_000_000, time.Now())
		raw, _ := json.Marshal(a)
		fn, args = "PublishRate", []string{string(raw)}
		rep.Attempt = fmt.Sprintf("BankFX publishes seq %d = 70.000000, validly signed by a colluding oracle key %s…", seq+1, rogue.PublicKey()[:12])

	case "stale-rate":
		raw, err := s.L.Evaluate(ledger.BankFX, "GetRates")
		if err != nil {
			return nil, err
		}
		var rates struct {
			Rates []attest.Attestation `json:"rates"`
		}
		if err := json.Unmarshal(raw, &rates); err != nil || len(rates.Rates) == 0 {
			return nil, fmt.Errorf("no published rate to replay")
		}
		old := rates.Rates[0] // the oldest genuine attestation (decoding drops ledger-only fields)
		b, _ := json.Marshal(old)
		fn, args = "PublishRate", []string{string(b)}
		rep.Attempt = fmt.Sprintf("BankFX re-publishes the genuine oracle rate seq %d (%d micros); the ledger is at seq %d", old.Seq, old.RateMicros, seq)

	case "out-of-band-rate":
		inr, err := s.quoteINR(10_000_00, seq)
		if err != nil {
			return nil, err
		}
		body := InstructionBody{As: "BANKIN", TradeID: newTradeID("ATK-OOB"), USDDeliverer: "BANKFX",
			USDAmount: itoa(10_000_00), INRAmount: itoa(inr * 105 / 100), RateSeq: seq}
		party, fn, args = ledger.BankIN, "SubmitInstruction", []string{instructionJSON(body)}
		rep.Attempt = fmt.Sprintf("BankIN instructs 10,000.00 USD for %d paise; the attested rate gives %d", inr*105/100, inr)

	case "negative-amount":
		body := InstructionBody{As: "BANKFX", TradeID: newTradeID("ATK-NEG"), USDDeliverer: "BANKFX",
			USDAmount: "-1000000", INRAmount: "83250000", RateSeq: seq}
		fn, args = "SubmitInstruction", []string{instructionJSON(body)}
		rep.Attempt = "BankFX instructs usdAmount = -1000000"

	case "overflow-amount":
		body := InstructionBody{As: "BANKFX", TradeID: newTradeID("ATK-OVF"), USDDeliverer: "BANKFX",
			USDAmount: "9223372036854775808", INRAmount: "1", RateSeq: seq}
		fn, args = "SubmitInstruction", []string{instructionJSON(body)}
		rep.Attempt = "BankFX instructs usdAmount = 9223372036854775808 (int64 max + 1)"

	case "reinit":
		req, _ := json.Marshal(map[string]any{
			"banks":           map[string]string{"BANKIN": s.L.MSPID(ledger.BankIN), "BANKFX": s.L.MSPID(ledger.BankFX)},
			"auditorMsps":     []string{},
			"pair":            "USD/INR",
			"oraclePublicKey": oracle.New().PublicKey(),
			"oracleName":      "BankFX Friendly Oracle",
			"rateWindow":      100,
			"balances": map[string]map[string]string{
				"BANKIN": {"INR": "0", "USD": "0"},
				"BANKFX": {"INR": "0", "USD": "999999999999999"},
			},
		})
		fn, args = "InitLedger", []string{string(req)}
		rep.Attempt = "BankFX calls InitLedger with its own oracle key and a 9.99 trillion USD balance"

	case "bank-offline":
		return s.bankOffline(rep)

	default:
		return nil, fmt.Errorf("attack %s not implemented", info.Name)
	}

	rep.Payload = args
	res, err := s.submitWithSnapshots(party, fn, args, opt, info.Name, rep.Attempt)
	if err != nil {
		return nil, err
	}
	rep.Result = res
	rep.Refused = !res.Outcome.OK
	rep.ExpectedCode = res.Outcome.Code == info.Expect
	return rep, nil
}

// bankOffline really stops BankFX's endorsing peer container, attempts a
// valid settlement as BankIN, then restarts the peer and waits for it.
func (s *Server) bankOffline(rep *AttackReport) (*AttackReport, error) {
	name := s.PeerContainers[ledger.BankFX]
	if name == "" {
		return nil, fmt.Errorf("bank-offline demo disabled: no peer container configured")
	}
	id := newTradeID("ATK-OFF")
	rep.TradeID = id
	if err := s.matchedTrade(&rep.Setup, id, "BANKFX", 100_00); err != nil {
		return nil, err
	}
	if out, err := docker("stop", name); err != nil {
		return nil, fmt.Errorf("docker stop %s: %v %s", name, err, out)
	}
	restart := func() string {
		if out, err := docker("start", name); err != nil {
			return fmt.Sprintf("RESTART FAILED: %v %s", err, out)
		}
		// Ready means BankIN's gateway can again collect endorsements from
		// BOTH orgs (service discovery has re-learned BankFX's peer). Probe
		// with an endorsement of a read-only call that is never submitted.
		start := time.Now()
		for time.Since(start) < 120*time.Second {
			if e, _ := s.L.Endorse(ledger.BankIN, "GetConfig"); e != nil {
				return fmt.Sprintf("BankFX peer restarted; both orgs endorsing again after %s", time.Since(start).Round(time.Second))
			}
			time.Sleep(3 * time.Second)
		}
		return "BankFX peer restarted but the network had not recovered after 120s"
	}

	rep.Payload = []string{id}
	rep.Attempt = fmt.Sprintf("docker stop %s, then BankIN calls SettleTrade(%s)", name, id)
	res, err := s.submitWithSnapshots(ledger.BankIN, "SettleTrade", []string{id}, ledger.SubmitOptions{}, rep.Name, rep.Attempt)
	rep.Note = restart()
	if err != nil {
		return nil, err
	}
	rep.Result = res
	rep.Refused = !res.Outcome.OK
	// Any endorsement-stage transport failure counts: the peer is gone.
	rep.ExpectedCode = !res.Outcome.OK && res.Outcome.Stage == "endorse"
	return rep, nil
}

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}
