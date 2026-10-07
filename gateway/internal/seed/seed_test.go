package seed

import (
	"os"
	"path/filepath"
	"testing"
)

func repoSeed(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", RelPath)
}

func TestLoadRepoSeed(t *testing.T) {
	s, err := Load(repoSeed(t))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Trades); n < 12 || n > 20 {
		t.Fatalf("%d trades, want 12-20", n)
	}
	if len(s.Scenarios) == 0 || len(s.OpeningBalances) != 4 {
		t.Fatalf("scenarios %d, opening balances for %d banks", len(s.Scenarios), len(s.OpeningBalances))
	}
}

func TestLoadRefusesBadSeeds(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown field": `{"trades":[],"scenarios":[],"savingsPct":90}`,
		"single-org":    `{"trades":[{"tradeId":"A","usdDeliverer":"BANKIN","inrDeliverer":"BANKUS","usdAmount":"1"}],"scenarios":[]}`,
		"unknown bank":  `{"trades":[{"tradeId":"A","usdDeliverer":"BANKXX","inrDeliverer":"BANKFX","usdAmount":"1"}],"scenarios":[]}`,
		"bad amount":    `{"trades":[{"tradeId":"A","usdDeliverer":"BANKIN","inrDeliverer":"BANKFX","usdAmount":"-5"}],"scenarios":[]}`,
		"duplicate":     `{"trades":[{"tradeId":"A","usdDeliverer":"BANKIN","inrDeliverer":"BANKFX","usdAmount":"1"},{"tradeId":"A","usdDeliverer":"BANKIN","inrDeliverer":"BANKFX","usdAmount":"1"}],"scenarios":[]}`,
		"unknown trade": `{"trades":[],"scenarios":[{"id":"s","tradeIds":["Z"]}]}`,
	} {
		p := filepath.Join(dir, "s.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestFindHonoursEnv(t *testing.T) {
	t.Setenv("LIQUIDITY_SEED", "/some/where.json")
	if p, _ := Find(); p != "/some/where.json" {
		t.Fatalf("got %s", p)
	}
}

func TestCustodian(t *testing.T) {
	for b, want := range map[string]string{"BANKIN": "BANKIN", "BANKUS": "BANKIN", "BANKFX": "BANKFX", "BANKSG": "BANKFX"} {
		if got := Custodian(b); got != want {
			t.Fatalf("Custodian(%s) = %s, want %s", b, got, want)
		}
	}
}
