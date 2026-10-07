package contract

import "github.com/advait/pvp-settlement/chaincode/attest"

// Bank roles. The chaincode never trusts a role claim on its own; it maps the
// submitter's MSP ID to a role using the config pinned at InitLedger.
const (
	BankIN = "BANKIN"
	BankFX = "BANKFX"
	BankUS = "BANKUS"
	BankSG = "BANKSG"
)

// Currencies held on-ledger (simulated tokenized cash).
const (
	INR = "INR"
	USD = "USD"
)

var (
	allBanks      = []string{BankIN, BankFX, BankUS, BankSG} // fixed order: determinism
	allCurrencies = []string{INR, USD}
)

func otherBank(b string) string {
	if b == BankIN {
		return BankFX
	}
	return BankIN
}

func isBank(b string) bool {
	return b == BankIN || b == BankFX || b == BankUS || b == BankSG
}

// Trade statuses.
const (
	StatusPendingMatch = "PENDING_MATCH" // one bank has instructed
	StatusMatched      = "MATCHED"       // both banks instructed identical terms
	StatusSettled      = "SETTLED"
)

// State key prefixes (composite-key object types).
const (
	keyConfig   = "CFG"
	keyBalance  = "BAL"  // BAL~<bank>~<ccy>
	keySupply   = "SUP"  // SUP~<ccy>  total units issued at init; never changes
	keyRate     = "RATE" // RATE~<seq 20-digit>
	keyRateHead = "RATEHEAD"
	keyTrade    = "TRADE" // TRADE~<tradeId>
	keyInstr    = "INSTR" // INSTR~<tradeId>~<bank>
	keyBatch    = "BATCH" // BATCH~<batchId>
	keyLogHead  = "LOGHEAD"
	keyLog      = "LOG" // LOG~<n 20-digit>  append-only audit log
)

// Config is pinned once at InitLedger and never changes.
type Config struct {
	Banks       map[string]string `json:"banks"`       // role -> MSP ID
	AuditorMSPs []string          `json:"auditorMsps"` // read-only orgs: refused on every write
	// OracleMSP, if set, is the only MSP allowed to submit PublishRate. The
	// attestation must still verify against OraclePublicKey, so publishing
	// needs both the oracle org's identity and the oracle's signing key.
	// Empty means any non-auditor channel member may relay a signed rate.
	OracleMSP       string `json:"oracleMsp,omitempty"`
	Pair            string `json:"pair"`            // "USD/INR"
	OraclePublicKey string `json:"oraclePublicKey"` // base64 Ed25519
	OracleName      string `json:"oracleName"`
	RateWindow      int    `json:"rateWindow"` // accept the latest N published rates
}

// InitRequest is the InitLedger argument.
type InitRequest struct {
	Config
	Balances map[string]map[string]string `json:"balances"` // bank -> ccy -> amount (minor units)
}

// Attestation is a signed FX rate from the Oracle (see package attest).
type Attestation = attest.Attestation

// RateRecord is a verified attestation as stored on the ledger.
type RateRecord struct {
	Attestation
	PublishedTx  string `json:"publishedTx"`
	PublishedBy  string `json:"publishedBy"`  // MSP that relayed it (anyone may; the signature is what counts)
	VerifiedWith string `json:"verifiedWith"` // oracle public key used
}

// Instruction is one bank's commitment to a trade's exact terms.
type Instruction struct {
	TradeID      string `json:"tradeId"`
	AsBank       string `json:"asBank"`       // logical bank participant instructing; must match submitter MSP authorization
	USDDeliverer string `json:"usdDeliverer"` // bank that pays USD
	INRDeliverer string `json:"inrDeliverer"` // bank that pays INR
	USDAmount    string `json:"usdAmount"`    // cents
	INRAmount    string `json:"inrAmount"`    // paise
	RateSeq      int64  `json:"rateSeq"`      // attestation the terms were priced at
}

// StoredInstruction is an instruction plus who submitted it and in which tx.
type StoredInstruction struct {
	Instruction
	SubmitterMSP string `json:"submitterMsp"`
	TxID         string `json:"txId"`
}

// Trade is the matched (and later settled) trade.
type Trade struct {
	TradeID      string   `json:"tradeId"`
	Status       string   `json:"status"`
	USDDeliverer string   `json:"usdDeliverer"`
	INRDeliverer string   `json:"inrDeliverer"`
	USDAmount    int64    `json:"usdAmount"`
	INRAmount    int64    `json:"inrAmount"`
	RateSeq      int64    `json:"rateSeq"`
	RateMicros   int64    `json:"rateMicros"`
	InstructedBy []string `json:"instructedBy"` // banks that have instructed, sorted
	SettledTx    string   `json:"settledTx,omitempty"`
	SettledVia   string   `json:"settledVia,omitempty"` // "GROSS" or "NET:<batchId>"
	// Real balances immediately before and after this trade settled (gross only).
	BalancesBefore map[string]map[string]int64 `json:"balancesBefore,omitempty"`
	BalancesAfter  map[string]map[string]int64 `json:"balancesAfter,omitempty"`
}

// LogEntry is one line of the append-only audit log.
type LogEntry struct {
	N            int64  `json:"n"`
	Type         string `json:"type"`
	TxID         string `json:"txId"`
	SubmitterMSP string `json:"submitterMsp"`
	TxTimestamp  string `json:"txTimestamp"` // proposal timestamp, display only
	Ref          string `json:"ref"`         // tradeId, batchId, or rate seq
	Detail       string `json:"detail"`
}

// ConservationReport is the live value-conservation check for one currency.
type ConservationReport struct {
	Currency string `json:"currency"`
	Supply   int64  `json:"supply"` // fixed at init
	Sum      int64  `json:"sum"`    // real sum over every balance on the ledger
	Holds    bool   `json:"holds"`
	Accounts int    `json:"accounts"` // number of balances summed
}
