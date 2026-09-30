// Package paths finds the Drunix crypto material and the oracle key the same
// way on every machine, so the gateway, pvpctl and the integration tests do
// not depend on the directory they are started from or on /root.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultDrunixHome is where the README clones Drunix (inside WSL, as root).
const DefaultDrunixHome = "/root/drunix"

// OrgsDir returns the test network's organizations directory:
// $DRUNIX_ORGS, else $DRUNIX_HOME/drunix-network/test-network/organizations,
// else the same under /root/drunix. network/add-orgs.sh and
// network/deploy-cc.sh honour DRUNIX_HOME the same way.
func OrgsDir() string {
	if v := os.Getenv("DRUNIX_ORGS"); v != "" {
		return v
	}
	home := os.Getenv("DRUNIX_HOME")
	if home == "" {
		home = DefaultDrunixHome
	}
	return filepath.Join(home, "drunix-network", "test-network", "organizations")
}

// CheckOrgsDir reports, with the fix, why the crypto material for the four
// orgs the gateway signs as is missing.
func CheckOrgsDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("Drunix crypto material not found at %s. Is the network up (network.sh up)? "+
			"If Drunix is not cloned at %s, set DRUNIX_HOME (or DRUNIX_ORGS) to where it is", dir, DefaultDrunixHome)
	}
	for _, org := range []string{"org1", "org2"} {
		if _, err := os.Stat(filepath.Join(dir, "peerOrganizations", org+".example.com")); err != nil {
			return fmt.Errorf("%s has no %s.example.com. Bring the network up first: ./network.sh up && ./network.sh createChannel", dir, org)
		}
	}
	for _, org := range []string{"oracle", "auditor"} {
		if _, err := os.Stat(filepath.Join(dir, "peerOrganizations", org+".example.com")); err != nil {
			return fmt.Errorf("%s has no %s.example.com. Add the Oracle and Auditor orgs: bash network/add-orgs.sh", dir, org)
		}
	}
	return nil
}

// OracleKey returns $ORACLE_KEY, else the first network/oracle/oracle.key
// found in the working directory or any parent, so the gateway works from
// the repo root, gateway/ or gateway/integration/.
func OracleKey() (string, error) {
	if v := os.Getenv("ORACLE_KEY"); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("ORACLE_KEY=%s: %w", v, err)
		}
		return v, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, "network", "oracle", "oracle.key")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("network/oracle/oracle.key not found (it is git-ignored, so a fresh clone does not have it). " +
		"From the repo root run: oracle keygen network/oracle   (then initialise a FRESH ledger with pvpctl init network/oracle/oracle.pub), " +
		"or set ORACLE_KEY to the key the ledger was initialised with")
}
