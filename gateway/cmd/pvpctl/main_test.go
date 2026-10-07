package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/advait/pvp-settlement/gateway/internal/seed"
)

// The seed scenarios assume pvpctl init's opening balances; the two must not drift.
func TestOpeningBalancesMatchSeed(t *testing.T) {
	s, err := seed.Load(filepath.Join("..", "..", "..", seed.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.OpeningBalances, openingBalances) {
		t.Fatalf("pvpctl openingBalances %v != seed openingBalances %v", openingBalances, s.OpeningBalances)
	}
}

// A re-run after an interrupted seed instructs only the side still missing.
func TestMissingSides(t *testing.T) {
	tr := seed.Trade{TradeID: "LQ01", USDDeliverer: "BANKIN", INRDeliverer: "BANKFX"}
	for _, c := range []struct {
		by   []string
		want []string
	}{
		{nil, []string{"BANKIN", "BANKFX"}},
		{[]string{"BANKIN"}, []string{"BANKFX"}},
		{[]string{"BANKFX"}, []string{"BANKIN"}},
		{[]string{"BANKFX", "BANKIN"}, nil},
	} {
		if got := missingSides(tr, c.by); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("instructedBy %v: got %v, want %v", c.by, got, c.want)
		}
	}
}
