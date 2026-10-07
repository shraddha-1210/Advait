// Command pvpctl runs one-off ledger operations against the Drunix network.
//
//	pvpctl init <oracle.pub>                      pin config + issue opening balances (once)
//	pvpctl publish <oracle.key> <rateMicros>      oracle-sign the next rate seq and publish it as OracleMSP
//	pvpctl query <Function> [args...]             evaluate a query as BankIN
//	pvpctl seed [liquidity_seed.json]             instruct the liquidity demo trades (both sides, skips existing)
//
// Env: DRUNIX_HOME (default /root/drunix), DRUNIX_ORGS (default $DRUNIX_HOME/drunix-network/test-network/organizations)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
	"github.com/advait/pvp-settlement/gateway/internal/paths"
	"github.com/advait/pvp-settlement/gateway/internal/seed"
)

// Opening balances for the demo (minor units). Simulated tokenized cash.
// BANKUS and BANKSG are simulated ledger-level participants within the
// existing two-org network: BANKUS is custodied by BankIN's org, BANKSG by
// BankFX's org. They are not orgs and have no peers or MSPs of their own.
var openingBalances = map[string]map[string]string{
	"BANKIN": {"INR": "50000000000", "USD": "0"}, // 500,000,000.00 INR
	"BANKFX": {"INR": "0", "USD": "500000000"},   //   5,000,000.00 USD
	"BANKUS": {"INR": "0", "USD": "100000000"},   //   1,000,000.00 USD
	"BANKSG": {"INR": "25000000000", "USD": "0"}, // 250,000,000.00 INR
}

// bankCustodians maps every ledger bank to the MSP of the org that custodies
// it. The chaincode requires BANKIN and BANKFX to be different orgs and every
// other bank to be custodied by one of them.
func bankCustodians(c *ledger.Client) map[string]string {
	return map[string]string{
		"BANKIN": c.MSPID(ledger.BankIN), "BANKUS": c.MSPID(ledger.BankIN),
		"BANKFX": c.MSPID(ledger.BankFX), "BANKSG": c.MSPID(ledger.BankFX),
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	orgs := paths.OrgsDir()
	if err := paths.CheckOrgsDir(orgs); err != nil {
		fail(err)
	}
	c, err := ledger.Connect(ledger.DefaultConfig(orgs))
	if err != nil {
		fail(err)
	}
	defer c.Close()

	switch os.Args[1] {
	case "init":
		if len(os.Args) != 3 {
			usage()
		}
		pub, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail(err)
		}
		// The key is pinned forever, so refuse a public key that this
		// machine's oracle.key cannot sign for (e.g. an oracle.pub pulled
		// from another PC): no rate could ever be published afterwards.
		if keyPath, err := paths.OracleKey(); err == nil {
			if s, err := oracle.Load(keyPath); err == nil && s.PublicKey() != strings.TrimSpace(string(pub)) {
				fail(fmt.Errorf("%s does not match %s (its public key is %s); regenerate both with: oracle keygen network/oracle",
					os.Args[2], keyPath, s.PublicKey()))
			}
		}
		req := map[string]any{
			"banks":           bankCustodians(c),
			"auditorMsps":     []string{c.MSPID(ledger.Auditor)},
			"oracleMsp":       c.MSPID(ledger.Oracle),
			"pair":            oracle.Pair,
			"oraclePublicKey": strings.TrimSpace(string(pub)),
			"oracleName":      oracle.Source,
			"rateWindow":      3,
			"balances":        openingBalances,
		}
		raw, _ := json.Marshal(req)
		report(c.Submit(ledger.BankIN, "InitLedger", []string{string(raw)}, ledger.SubmitOptions{}))
	case "publish":
		if len(os.Args) != 4 {
			usage()
		}
		s, err := oracle.Load(os.Args[2])
		if err != nil {
			fail(err)
		}
		rate, err := strconv.ParseInt(os.Args[3], 10, 64)
		if err != nil {
			usage()
		}
		raw, err := c.Evaluate(ledger.BankIN, "GetRates")
		if err != nil {
			fail(err)
		}
		var rates struct{ Head int64 }
		_ = json.Unmarshal(raw, &rates)
		att, _ := json.Marshal(s.Sign(rates.Head+1, rate, time.Now()))
		report(c.Submit(ledger.Oracle, "PublishRate", []string{string(att)}, ledger.SubmitOptions{}))
	case "seed":
		path := ""
		if len(os.Args) == 3 {
			path = os.Args[2]
		} else if len(os.Args) == 2 {
			if path, err = seed.Find(); err != nil {
				fail(err)
			}
		} else {
			usage()
		}
		if err := runSeed(c, path); err != nil {
			fail(err)
		}
	case "query":
		if len(os.Args) < 3 {
			usage()
		}
		out, err := c.Evaluate(ledger.BankIN, os.Args[2], os.Args[3:]...)
		if err != nil {
			fail(err)
		}
		fmt.Println(string(out))
	default:
		usage()
	}
}

func report(o ledger.Outcome) {
	b, _ := json.MarshalIndent(o, "", "  ")
	fmt.Println(string(b))
	if !o.OK {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pvpctl init <oracle.pub> | publish <oracle.key> <rateMicros> | query <Function> [args...] | seed [liquidity_seed.json]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// runSeed instructs every seed trade on the ledger, each side by the org that
// custodies it, priced by the chaincode's own QuoteINR at the latest attested
// rate. It can be re-run: a MATCHED or SETTLED trade is skipped, and a trade
// left PENDING_MATCH by an interrupted run gets only its missing side,
// instructed with the terms already on the ledger.
func runSeed(c *ledger.Client, path string) error {
	s, err := seed.Load(path)
	if err != nil {
		return err
	}
	raw, err := c.Evaluate(ledger.BankIN, "GetRates")
	if err != nil {
		return err
	}
	var rates struct{ Head int64 }
	if err := json.Unmarshal(raw, &rates); err != nil || rates.Head < 1 {
		return fmt.Errorf("no attested rate on the ledger; run pvpctl publish first")
	}
	seq := strconv.FormatInt(rates.Head, 10)
	for _, t := range s.Trades {
		var existing struct {
			Trade struct {
				Status       string   `json:"status"`
				InstructedBy []string `json:"instructedBy"`
				INRAmount    int64    `json:"inrAmount"`
				RateSeq      int64    `json:"rateSeq"`
			} `json:"trade"`
		}
		inr, rateSeq := int64(0), rates.Head
		out, err := c.Evaluate(ledger.BankIN, "GetTrade", t.TradeID)
		switch {
		case err == nil:
			if err := json.Unmarshal(out, &existing); err != nil {
				return fmt.Errorf("GetTrade %s: %w", t.TradeID, err)
			}
			if existing.Trade.Status != "PENDING_MATCH" {
				fmt.Printf("%s already %s, skipped\n", t.TradeID, existing.Trade.Status)
				continue
			}
			// Resume an interrupted run with the terms already on the ledger.
			inr, rateSeq = existing.Trade.INRAmount, existing.Trade.RateSeq
		case strings.Contains(err.Error(), "ERR_TRADE_NOT_FOUND"):
			q, err := c.Evaluate(ledger.BankIN, "QuoteINR", t.USDAmount, seq)
			if err != nil {
				return fmt.Errorf("QuoteINR for %s: %w", t.TradeID, err)
			}
			var quote struct {
				INRAmount int64 `json:"inrAmount"`
			}
			if err := json.Unmarshal(q, &quote); err != nil {
				return fmt.Errorf("QuoteINR for %s: %w", t.TradeID, err)
			}
			inr = quote.INRAmount
		default:
			return fmt.Errorf("GetTrade %s: %w", t.TradeID, err)
		}
		for _, side := range missingSides(t, existing.Trade.InstructedBy) {
			in, _ := json.Marshal(map[string]any{
				"tradeId": t.TradeID, "asBank": side,
				"usdDeliverer": t.USDDeliverer, "inrDeliverer": t.INRDeliverer,
				"usdAmount": t.USDAmount, "inrAmount": strconv.FormatInt(inr, 10), "rateSeq": rateSeq,
			})
			o := c.Submit(ledger.Party(seed.Custodian(side)), "SubmitInstruction", []string{string(in)}, ledger.SubmitOptions{})
			if !o.OK {
				return fmt.Errorf("%s instruction for %s refused: %s %s", side, t.TradeID, o.Code, o.Message)
			}
		}
		fmt.Printf("%s matched: %s pays %s USD cents, %s pays %d INR paise\n", t.TradeID, t.USDDeliverer, t.USDAmount, t.INRDeliverer, inr)
	}
	return nil
}

// missingSides lists the sides of seed trade t that have not instructed yet,
// USD deliverer first.
func missingSides(t seed.Trade, instructedBy []string) []string {
	var out []string
	for _, side := range []string{t.USDDeliverer, t.INRDeliverer} {
		done := false
		for _, b := range instructedBy {
			done = done || b == side
		}
		if !done {
			out = append(out, side)
		}
	}
	return out
}
