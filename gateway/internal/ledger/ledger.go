// Package ledger connects to the Drunix network through the Fabric Gateway
// service on each bank's lite peer and submits/evaluates chaincode calls.
//
// Demo simplification (stated in the README): this one process holds a
// client identity for each bank so a single UI can drive both sides. In a
// real deployment each bank signs with its own keys inside its own systems.
package ledger

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/hash"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	gwproto "github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// Party is who a call is made as.
type Party string

const (
	BankIN Party = "BANKIN"
	BankFX Party = "BANKFX"
)

// PartyConfig describes one client identity and the peer it talks to.
type PartyConfig struct {
	Party        Party
	MSPID        string
	CertPath     string // PEM certificate (file or directory containing one)
	KeyPath      string // PEM private key (file or directory containing one)
	TLSCACert    string // TLS CA of the gateway peer
	PeerEndpoint string // host:port of the org's lite peer
	PeerHostname string // TLS server name
}

type Config struct {
	Channel   string
	Chaincode string
	Parties   []PartyConfig
}

// DefaultConfig matches the Drunix test network (see NOTES.md D3):
// Org1 lite peer :7051, Org2 lite peer :9051.
func DefaultConfig(orgsDir string) Config {
	p := func(party Party, msp, org, port string) PartyConfig {
		base := filepath.Join(orgsDir, "peerOrganizations", org+".example.com")
		user := filepath.Join(base, "users", "User1@"+org+".example.com", "msp")
		return PartyConfig{
			Party: party, MSPID: msp,
			CertPath:     filepath.Join(user, "signcerts"),
			KeyPath:      filepath.Join(user, "keystore"),
			TLSCACert:    filepath.Join(base, "peers", "peer0."+org+".example.com", "tls", "ca.crt"),
			PeerEndpoint: "localhost:" + port,
			PeerHostname: "peer0." + org + ".example.com",
		}
	}
	return Config{
		Channel: "mychannel", Chaincode: "pvp",
		Parties: []PartyConfig{
			p(BankIN, "Org1MSP", "org1", "7051"),
			p(BankFX, "Org2MSP", "org2", "9051"),
		},
	}
}

type conn struct {
	cfg      PartyConfig
	grpc     *grpc.ClientConn
	gw       *client.Gateway
	contract *client.Contract
}

// Client holds one gateway connection per party.
type Client struct {
	cfg   Config
	conns map[Party]*conn
	// Submissions are serialised: every value-moving transaction reads the
	// same balance keys, so concurrent submits would only collide on MVCC.
	submitMu sync.Mutex
}

func Connect(cfg Config) (*Client, error) {
	c := &Client{cfg: cfg, conns: map[Party]*conn{}}
	for _, pc := range cfg.Parties {
		cn, err := dial(cfg, pc)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("%s: %w", pc.Party, err)
		}
		c.conns[pc.Party] = cn
	}
	return c, nil
}

func (c *Client) Close() {
	for _, cn := range c.conns {
		cn.gw.Close()
		cn.grpc.Close()
	}
}

func (c *Client) MSPID(p Party) string {
	if cn, ok := c.conns[p]; ok {
		return cn.cfg.MSPID
	}
	return ""
}

func dial(cfg Config, pc PartyConfig) (*conn, error) {
	caPEM, err := os.ReadFile(pc.TLSCACert)
	if err != nil {
		return nil, fmt.Errorf("read TLS CA: %w", err)
	}
	ca, err := identity.CertificateFromPEM(caPEM)
	if err != nil {
		return nil, fmt.Errorf("parse TLS CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	gc, err := grpc.NewClient("dns:///"+pc.PeerEndpoint,
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, pc.PeerHostname)))
	if err != nil {
		return nil, fmt.Errorf("grpc: %w", err)
	}

	certPEM, err := readFirst(pc.CertPath)
	if err != nil {
		return nil, err
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	id, err := identity.NewX509Identity(pc.MSPID, cert)
	if err != nil {
		return nil, err
	}
	keyPEM, err := readFirst(pc.KeyPath)
	if err != nil {
		return nil, err
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	sign, err := identity.NewPrivateKeySign(key)
	if err != nil {
		return nil, err
	}
	gw, err := client.Connect(id,
		client.WithSign(sign), client.WithHash(hash.SHA256), client.WithClientConnection(gc),
		client.WithEvaluateTimeout(10*time.Second),
		client.WithEndorseTimeout(20*time.Second),
		client.WithSubmitTimeout(10*time.Second),
		client.WithCommitStatusTimeout(60*time.Second),
	)
	if err != nil {
		gc.Close()
		return nil, err
	}
	return &conn{cfg: pc, grpc: gc, gw: gw, contract: gw.GetNetwork(cfg.Channel).GetContract(cfg.Chaincode)}, nil
}

func readFirst(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return os.ReadFile(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			return os.ReadFile(filepath.Join(path, e.Name()))
		}
	}
	return nil, fmt.Errorf("no file in %s", path)
}

// ---------------------------------------------------------------------------

// Outcome is the real result of one chaincode call, as the network reported it.
type Outcome struct {
	OK       bool   `json:"ok"`
	TxID     string `json:"txId,omitempty"`
	Party    Party  `json:"party"`
	MSPID    string `json:"mspId"`
	Function string `json:"function"`
	// Stage where a rejection happened:
	//   "endorse" - the chaincode on an endorsing peer refused (Code = ERR_...)
	//   "commit"  - the network's validation refused (Code = TxValidationCode)
	//   "submit"/"connect" - transport problems (peer down, timeout)
	Stage       string   `json:"stage,omitempty"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message,omitempty"`
	Endorsers   []string `json:"endorsingOrgs,omitempty"` // only when restricted
	BlockNumber uint64   `json:"blockNumber,omitempty"`
	Result      string   `json:"result,omitempty"`
}

var codeRE = regexp.MustCompile(`ERR_[A-Z_]+`)

// SubmitOptions restricts endorsement to specific orgs (used to demonstrate
// that one org's endorsement alone cannot satisfy AND(BankIN, BankFX)).
type SubmitOptions struct {
	EndorsingOrgs []string
}

// Submit endorses, orders and waits for commit of fn(args) as party.
func (c *Client) Submit(p Party, fn string, args []string, opt SubmitOptions) Outcome {
	c.submitMu.Lock()
	defer c.submitMu.Unlock()
	out := Outcome{Party: p, Function: fn, MSPID: c.MSPID(p), Endorsers: opt.EndorsingOrgs}
	cn, ok := c.conns[p]
	if !ok {
		out.Stage, out.Code, out.Message = "connect", "NO_IDENTITY", fmt.Sprintf("no identity configured for %s", p)
		return out
	}
	popts := []client.ProposalOption{client.WithArguments(args...)}
	if len(opt.EndorsingOrgs) > 0 {
		popts = append(popts, client.WithEndorsingOrganizations(opt.EndorsingOrgs...))
	}
	prop, err := cn.contract.NewProposal(fn, popts...)
	if err != nil {
		out.Stage, out.Code, out.Message = "endorse", "PROPOSAL_ERROR", err.Error()
		return out
	}
	out.TxID = prop.TransactionID()
	tx, err := prop.Endorse()
	if err != nil {
		out.Stage = "endorse"
		out.Code, out.Message = decodeEndorseError(err)
		return out
	}
	out.Result = string(tx.Result())
	commit, err := tx.Submit()
	if err != nil {
		out.Stage, out.Code, out.Message = "submit", "SUBMIT_FAILED", err.Error()
		return out
	}
	st, err := commit.Status()
	if err != nil {
		out.Stage, out.Code, out.Message = "commit", "COMMIT_STATUS_UNAVAILABLE", err.Error()
		return out
	}
	out.BlockNumber = st.BlockNumber
	if !st.Successful {
		out.Stage, out.Code = "commit", st.Code.String()
		out.Message = fmt.Sprintf("transaction %s was ordered into block %d but INVALIDATED by the network: %s. None of its writes were applied.",
			out.TxID, st.BlockNumber, st.Code.String())
		return out
	}
	out.OK = true
	return out
}

// Endorsed is a transaction that has been endorsed but not yet submitted.
type Endorsed struct {
	tx  *client.Transaction
	out Outcome
}

// Endorse runs only the endorsement phase. Used by the concurrency test to
// endorse two conflicting transactions against the SAME committed state
// before either is ordered, which is exactly how a double-spend race looks.
// It bypasses the submit mutex on purpose.
func (c *Client) Endorse(p Party, fn string, args ...string) (*Endorsed, Outcome) {
	out := Outcome{Party: p, Function: fn, MSPID: c.MSPID(p)}
	cn, ok := c.conns[p]
	if !ok {
		out.Stage, out.Code = "connect", "NO_IDENTITY"
		return nil, out
	}
	prop, err := cn.contract.NewProposal(fn, client.WithArguments(args...))
	if err != nil {
		out.Stage, out.Code, out.Message = "endorse", "PROPOSAL_ERROR", err.Error()
		return nil, out
	}
	out.TxID = prop.TransactionID()
	tx, err := prop.Endorse()
	if err != nil {
		out.Stage = "endorse"
		out.Code, out.Message = decodeEndorseError(err)
		return nil, out
	}
	out.OK = true
	return &Endorsed{tx: tx, out: out}, out
}

// SubmitEndorsed orders a previously endorsed transaction and waits for its
// commit status.
func (e *Endorsed) Submit() Outcome {
	out := e.out
	out.OK = false
	commit, err := e.tx.Submit()
	if err != nil {
		out.Stage, out.Code, out.Message = "submit", "SUBMIT_FAILED", err.Error()
		return out
	}
	st, err := commit.Status()
	if err != nil {
		out.Stage, out.Code, out.Message = "commit", "COMMIT_STATUS_UNAVAILABLE", err.Error()
		return out
	}
	out.BlockNumber = st.BlockNumber
	if !st.Successful {
		out.Stage, out.Code = "commit", st.Code.String()
		out.Message = "invalidated by the network: " + st.Code.String()
		return out
	}
	out.OK = true
	return out
}

// Evaluate runs a read-only query as party.
func (c *Client) Evaluate(p Party, fn string, args ...string) ([]byte, error) {
	cn, ok := c.conns[p]
	if !ok {
		return nil, fmt.Errorf("no identity configured for %s", p)
	}
	res, err := cn.contract.EvaluateTransaction(fn, args...)
	if err != nil {
		code, msg := decodeEndorseError(err)
		return nil, fmt.Errorf("%s: %s", code, msg)
	}
	return res, nil
}

// decodeEndorseError pulls the chaincode's own rejection out of the gRPC
// error details that the gateway returns.
func decodeEndorseError(err error) (code, msg string) {
	var details []string
	if st, ok := status.FromError(unwrapGRPC(err)); ok {
		for _, d := range st.Details() {
			if ed, ok := d.(*gwproto.ErrorDetail); ok {
				details = append(details, fmt.Sprintf("%s (%s): %s", ed.GetMspId(), ed.GetAddress(), ed.GetMessage()))
			}
		}
		if len(details) == 0 {
			details = append(details, st.Message())
		}
	} else {
		details = append(details, err.Error())
	}
	msg = strings.Join(details, " | ")
	if m := codeRE.FindString(msg); m != "" {
		code = m
		// Present the chaincode's message without transport noise.
		if i := strings.Index(msg, m); i >= 0 {
			rest := msg[i:]
			if j := strings.Index(rest, " | "); j >= 0 {
				rest = rest[:j]
			}
			return code, rest
		}
		return code, msg
	}
	switch {
	case strings.Contains(msg, "failed to collect enough transaction endorsements"),
		strings.Contains(msg, "no peers available"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "Unavailable"):
		return "ENDORSER_UNAVAILABLE", msg
	}
	return "ENDORSE_FAILED", msg
}

func unwrapGRPC(err error) error {
	var te *client.TransactionError
	if errors.As(err, &te) {
		return te
	}
	return err
}
