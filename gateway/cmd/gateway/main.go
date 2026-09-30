// Command gateway serves the settlement API on :8080 (run inside WSL).
//
// Env:
//
//	DRUNIX_HOME   Drunix clone      (default /root/drunix)
//	DRUNIX_ORGS   crypto material   (default $DRUNIX_HOME/drunix-network/test-network/organizations)
//	ORACLE_KEY    oracle seed file  (default: network/oracle/oracle.key in the working directory or a parent)
//	ADDR          listen address    (default :8080)
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/advait/pvp-settlement/gateway/internal/api"
	"github.com/advait/pvp-settlement/gateway/internal/ledger"
	"github.com/advait/pvp-settlement/gateway/internal/oracle"
	"github.com/advait/pvp-settlement/gateway/internal/paths"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	orgs := paths.OrgsDir()
	if err := paths.CheckOrgsDir(orgs); err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	keyPath, err := paths.OracleKey()
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	o, err := oracle.Load(keyPath)
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	l, err := ledger.Connect(ledger.DefaultConfig(orgs))
	if err != nil {
		log.Fatalf("cannot start: connect to Drunix using %s: %v", orgs, err)
	}
	defer l.Close()
	log.Printf("crypto material: %s", orgs)
	log.Printf("oracle key: %s (public %s)", keyPath, o.PublicKey())
	checkPinnedOracle(l, o)

	srv := &api.Server{
		L: l, Oracle: o,
		// Drunix test network: BankFX = Org2, whose endorsing lite peer is lp1.org2 (NOTES.md D3).
		PeerContainers: map[ledger.Party]string{ledger.BankFX: env("BANKFX_PEER_CONTAINER", "lp1.org2")},
	}
	addr := env("ADDR", ":8080")
	log.Printf("PvP settlement gateway on %s (BankIN=%s, BankFX=%s)", addr, l.MSPID(ledger.BankIN), l.MSPID(ledger.BankFX))
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

// checkPinnedOracle warns (it does not stop the gateway) when the ledger
// cannot be read yet, or when it was initialised with a different oracle key
// than this machine's oracle.key. Each clone generates its own key, so this
// is the usual reason rate publishing fails on a second PC.
func checkPinnedOracle(l *ledger.Client, o *oracle.Signer) {
	raw, err := l.Evaluate(ledger.BankIN, "GetConfig")
	if err != nil {
		log.Printf("WARNING: could not read the ledger config (%v). Is the network up, the chaincode deployed (network/deploy-cc.sh) and the ledger initialised (pvpctl init)? The gateway will start anyway; /api/health shows the state.", err)
		return
	}
	var cfg struct {
		OraclePublicKey string `json:"oraclePublicKey"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.OraclePublicKey == "" {
		return
	}
	if cfg.OraclePublicKey != o.PublicKey() {
		log.Printf("WARNING: this ledger was initialised with oracle public key %s, but this machine's oracle key is %s. "+
			"Every rate publish will be refused with ERR_ATTESTATION_BAD_SIGNATURE. Use the oracle.key the ledger was initialised with "+
			"(ORACLE_KEY=...), or start a fresh network and run pvpctl init with this machine's oracle.pub.",
			cfg.OraclePublicKey, o.PublicKey())
	}
}
