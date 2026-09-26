// Command gateway serves the settlement API on :8080 (run inside WSL).
//
// Env:
//
//	DRUNIX_ORGS   crypto material  (default /root/drunix/drunix-network/test-network/organizations)
//	ORACLE_KEY    oracle seed file (default ../network/oracle/oracle.key)
//	ADDR          listen address   (default :8080)
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/advait/pvp-settlement/gateway/internal/api"
	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	l, err := ledger.Connect(ledger.DefaultConfig(env("DRUNIX_ORGS", "/root/drunix/drunix-network/test-network/organizations")))
	if err != nil {
		log.Fatalf("connect to Drunix: %v", err)
	}
	defer l.Close()
	o, err := oracle.Load(env("ORACLE_KEY", "../network/oracle/oracle.key"))
	if err != nil {
		log.Fatalf("oracle: %v", err)
	}
	srv := &api.Server{
		L: l, Oracle: o,
		// Drunix test network: BankFX = Org2, whose endorsing lite peer is lp1.org2 (NOTES.md D3).
		PeerContainers: map[ledger.Party]string{ledger.BankFX: env("BANKFX_PEER_CONTAINER", "lp1.org2")},
	}
	addr := env("ADDR", ":8080")
	log.Printf("PvP settlement gateway on %s (BankIN=%s, BankFX=%s)", addr, l.MSPID(ledger.BankIN), l.MSPID(ledger.BankFX))
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
