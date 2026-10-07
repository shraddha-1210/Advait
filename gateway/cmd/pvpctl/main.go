// Command pvpctl runs one-off ledger operations against the Drunix network.
//
//	pvpctl init <oracle.pub>                      pin config + issue opening balances (once)
//	pvpctl publish <oracle.key> <rateMicros>      oracle-sign the next rate seq and publish it as OracleMSP
//	pvpctl query <Function> [args...]             evaluate a query as BankIN
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
	fmt.Fprintln(os.Stderr, "usage: pvpctl init <oracle.pub> | publish <oracle.key> <rateMicros> | query <Function> [args...]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
