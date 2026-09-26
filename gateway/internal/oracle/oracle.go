// Package oracle is the SIMULATED FX oracle. It holds an Ed25519 key and
// signs USD/INR rate attestations. The chaincode pins this key's public half
// at InitLedger and refuses any rate it did not sign.
//
// Rates are whatever the operator publishes; there is no live market feed.
package oracle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/advait/pvp-settlement/chaincode/attest"
)

const (
	Pair   = "USD/INR"
	Source = "Demo FX Oracle (simulated)"
)

type Signer struct {
	key ed25519.PrivateKey
}

// Load reads a base64 Ed25519 seed from path.
func Load(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read oracle key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("oracle key %s is not a base64 Ed25519 seed", path)
	}
	return &Signer{key: ed25519.NewKeyFromSeed(seed)}, nil
}

// New returns a signer with a fresh random key. Used to simulate a
// colluding "fake oracle" whose key was never pinned on the ledger.
func New() *Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return &Signer{key: priv}
}

// Keygen writes oracle.key (private seed, keep secret) and oracle.pub.
func Keygen(dir string) (pub string, err error) {
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "oracle.key"), []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return "", err
	}
	pub = base64.StdEncoding.EncodeToString(pubKey)
	return pub, os.WriteFile(filepath.Join(dir, "oracle.pub"), []byte(pub+"\n"), 0o644)
}

func (s *Signer) PublicKey() string {
	return base64.StdEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Sign produces a signed attestation for rate seq at rateMicros.
func (s *Signer) Sign(seq, rateMicros int64, asOf time.Time) attest.Attestation {
	a := attest.Attestation{
		Pair: Pair, Seq: seq, RateMicros: rateMicros, Source: Source,
		AsOf: asOf.UTC().Format(time.RFC3339),
	}
	a.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(s.key, attest.CanonicalPayload(a)))
	return a
}
