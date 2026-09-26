package contract

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"

	"github.com/advait/pvp-settlement/chaincode/attest"
)

// CanonicalPayload is the exact byte string the Oracle signs (shared with the
// off-chain signer via package attest). safeField below rejects '|' and
// control characters, so no field can smuggle in another field's value.
func CanonicalPayload(a Attestation) []byte { return attest.CanonicalPayload(a) }

func safeField(name, v string, required bool) error {
	if required && v == "" {
		return reject(ErrInvalidInput, "attestation %s is empty", name)
	}
	if len(v) > 128 {
		return reject(ErrInvalidInput, "attestation %s is longer than 128 bytes", name)
	}
	for _, r := range v {
		if r == '|' || r < 0x20 || r == 0x7f {
			return reject(ErrInvalidInput, "attestation %s contains a forbidden character", name)
		}
	}
	return nil
}

// verifyAttestation checks an attestation against the pinned oracle key.
// It does not check freshness; callers do that against ledger state.
func verifyAttestation(cfg *Config, a Attestation) error {
	if strings.TrimSpace(a.Signature) == "" {
		return reject(ErrAttestationUnsigned, "FX rate seq %d carries no oracle signature; unsigned rates are refused", a.Seq)
	}
	if a.Pair != cfg.Pair {
		return reject(ErrInvalidInput, "attestation pair %q does not match configured pair %q", a.Pair, cfg.Pair)
	}
	if a.Seq < 1 {
		return reject(ErrInvalidInput, "attestation seq must be >= 1, got %d", a.Seq)
	}
	if a.RateMicros <= 0 || a.RateMicros > MaxRateMicros {
		return reject(ErrInvalidInput, "attestation rateMicros %d is out of range (1..%d)", a.RateMicros, MaxRateMicros)
	}
	if err := safeField("source", a.Source, true); err != nil {
		return err
	}
	if err := safeField("asOf", a.AsOf, false); err != nil {
		return err
	}

	pub, err := base64.StdEncoding.DecodeString(cfg.OraclePublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return reject(ErrInternal, "pinned oracle public key is invalid")
	}
	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return reject(ErrAttestationBadSignature, "FX rate seq %d has a malformed signature", a.Seq)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), CanonicalPayload(a), sig) {
		return reject(ErrAttestationBadSignature,
			"FX rate seq %d (rate %d) is not signed by the pinned oracle key; tampered or forged rate refused",
			a.Seq, a.RateMicros)
	}
	return nil
}
