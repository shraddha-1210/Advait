// Package seed reads the deterministic liquidity-engine demo data
// (chaincode/pvp/contract/testdata/liquidity_seed.json). The same file drives
// the chaincode's seed test, pvpctl seed and the frontend's scenario list. It
// holds trade definitions and groupings only: every INR leg, net figure,
// dropped trade, cycle and saving is computed by the chaincode.
package seed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// RelPath is the seed file's location relative to the repo root.
var RelPath = filepath.Join("chaincode", "pvp", "contract", "testdata", "liquidity_seed.json")

type Trade struct {
	TradeID      string `json:"tradeId"`
	USDDeliverer string `json:"usdDeliverer"`
	INRDeliverer string `json:"inrDeliverer"`
	USDAmount    string `json:"usdAmount"` // cents
}

type Scenario struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Note     string   `json:"note"`
	TradeIDs []string `json:"tradeIds"`
}

type Seed struct {
	Description     string                       `json:"description"`
	OpeningBalances map[string]map[string]string `json:"openingBalances"`
	Trades          []Trade                      `json:"trades"`
	Scenarios       []Scenario                   `json:"scenarios"`
}

var banks = map[string]bool{"BANKIN": true, "BANKFX": true, "BANKUS": true, "BANKSG": true}

// Custodian is the org (BANKIN or BANKFX) that instructs for ledger bank b:
// BANKUS and BANKSG are simulated ledger-level participants custodied by
// BankIN's and BankFX's orgs.
func Custodian(b string) string {
	if b == "BANKIN" || b == "BANKUS" {
		return "BANKIN"
	}
	return "BANKFX"
}

// Load reads and checks a seed file. Unknown fields are refused.
func Load(path string) (*Seed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Seed
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, s.check()
}

func (s *Seed) check() error {
	ids := map[string]bool{}
	for _, t := range s.Trades {
		if t.TradeID == "" || ids[t.TradeID] {
			return fmt.Errorf("seed: empty or duplicate trade ID %q", t.TradeID)
		}
		ids[t.TradeID] = true
		if !banks[t.USDDeliverer] || !banks[t.INRDeliverer] {
			return fmt.Errorf("seed: trade %s names an unknown bank", t.TradeID)
		}
		if Custodian(t.USDDeliverer) == Custodian(t.INRDeliverer) {
			return fmt.Errorf("seed: trade %s has both sides custodied by %s; the chaincode refuses single-org trades", t.TradeID, Custodian(t.USDDeliverer))
		}
		if v, err := strconv.ParseInt(t.USDAmount, 10, 64); err != nil || v <= 0 {
			return fmt.Errorf("seed: trade %s usdAmount %q is not a positive integer", t.TradeID, t.USDAmount)
		}
	}
	for _, sc := range s.Scenarios {
		if sc.ID == "" || len(sc.TradeIDs) == 0 {
			return errors.New("seed: a scenario needs an id and trade IDs")
		}
		for _, id := range sc.TradeIDs {
			if !ids[id] {
				return fmt.Errorf("seed: scenario %s names unknown trade %s", sc.ID, id)
			}
		}
	}
	return nil
}

// Find locates the seed file: $LIQUIDITY_SEED, else RelPath in the current
// directory or any parent.
func Find() (string, error) {
	if v := os.Getenv("LIQUIDITY_SEED"); v != "" {
		return v, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, RelPath)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found from the current directory or its parents; set LIQUIDITY_SEED", RelPath)
		}
		dir = parent
	}
}
