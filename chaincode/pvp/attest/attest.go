// Package attest defines the Oracle's signed FX-rate attestation and the exact
// bytes that are signed. It has no dependencies so that the chaincode (which
// verifies) and the off-chain oracle signer (which signs) share one definition.
package attest

import (
	"strconv"
	"strings"
)

// Domain separates these signatures from any other use of the oracle key.
const Domain = "PVP-FX-ATTESTATION|v1"

// Attestation is a signed FX rate from the Oracle.
// The signature covers CanonicalPayload(), never the JSON encoding.
type Attestation struct {
	Pair       string `json:"pair"`
	Seq        int64  `json:"seq"`
	RateMicros int64  `json:"rateMicros"`
	Source     string `json:"source"`
	AsOf       string `json:"asOf"`      // informational only; chaincode never reads clocks
	Signature  string `json:"signature"` // base64 Ed25519 over CanonicalPayload()
}

// CanonicalPayload is the exact byte string the Oracle signs. The chaincode
// rejects string fields containing '|' or control characters, so this
// encoding is unambiguous.
func CanonicalPayload(a Attestation) []byte {
	return []byte(strings.Join([]string{
		Domain,
		a.Pair,
		strconv.FormatInt(a.Seq, 10),
		strconv.FormatInt(a.RateMicros, 10),
		a.Source,
		a.AsOf,
	}, "|"))
}
