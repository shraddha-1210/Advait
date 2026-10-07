package ledger

import "testing"

func TestChaincodeNameFromEnv(t *testing.T) {
	t.Setenv("CHAINCODE_NAME", "")
	if got := ChaincodeName(); got != "pvp" {
		t.Fatalf("default = %q, want pvp", got)
	}
	t.Setenv("CHAINCODE_NAME", " pvp-le ")
	if got := ChaincodeName(); got != "pvp-le" {
		t.Fatalf("got %q, want pvp-le", got)
	}
	if got := DefaultConfig("/x").Chaincode; got != "pvp-le" {
		t.Fatalf("DefaultConfig.Chaincode = %q, want pvp-le", got)
	}
}
