// Command pvpctl runs one-off ledger operations against the Drunix network.
//
//	pvpctl init <oracle.pub>                      pin config + issue opening balances (once)
//	pvpctl publish <oracle.key> <rateMicros>      oracle-sign the next rate seq and publish it as OracleMSP
//	pvpctl query <Function> [args...]             evaluate a query as BankIN
//
// Env: DRUNIX_ORGS (default /root/drunix/drunix-network/test-network/organizations)
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
)

// Opening balances for the demo (minor units). Simulated tokenized cash.
var openingBalances = map[string]map[string]string{
	"BANKIN": {"INR": "50000000000", "USD": "0"}, // 500,000,000.00 INR
	"BANKFX": {"INR": "0", "USD": "500000000"},   //   5,000,000.00 USD
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	orgs := os.Getenv("DRUNIX_ORGS")
	if orgs == "" {
		orgs = "/root/drunix/drunix-network/test-network/organizations"
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
		req := map[string]any{
			"banks":           map[string]string{"BANKIN": c.MSPID(ledger.BankIN), "BANKFX": c.MSPID(ledger.BankFX)},
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
