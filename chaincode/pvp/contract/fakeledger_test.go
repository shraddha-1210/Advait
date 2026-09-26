package contract

import (
	"crypto/x509"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/queryresult"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeLedger is an in-memory world state with Fabric transaction semantics
// that matter for these tests:
//
//   - A transaction's writes are buffered and applied only if the chaincode
//     function returns nil. A rejected call leaves committed state untouched,
//     exactly as Fabric discards the write set of a failed endorsement.
//   - GetState and range queries read COMMITTED state only. Fabric does not
//     let a transaction read its own writes; a chaincode bug that relies on
//     doing so will fail these tests the same way it would fail on a peer.
//
// It is not a mock: there are no canned return values. Balances, sums and
// rejections all come from the real chaincode running against this state.
type fakeLedger struct {
	committed map[string][]byte
	txCounter int
	clock     time.Time
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{committed: map[string][]byte{}, clock: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
}

// txResult reports what one invocation tried to do.
type txResult struct {
	TxID      string
	Err       error
	Writes    map[string][]byte // attempted writes (applied only if Err == nil)
	Events    map[string][]byte
	Committed bool
}

// invoke runs fn as one transaction submitted by an identity from msp.
func (l *fakeLedger) invoke(msp string, fn func(ctx contractapi.TransactionContextInterface) error) txResult {
	l.txCounter++
	l.clock = l.clock.Add(time.Second)
	stub := &txStub{ledger: l, txID: fmt.Sprintf("tx%04d", l.txCounter), ts: l.clock,
		writes: map[string][]byte{}, events: map[string][]byte{}}
	ctx := &contractapi.TransactionContext{}
	ctx.SetStub(stub)
	ctx.SetClientIdentity(fakeIdentity{msp: msp})

	err := fn(ctx)
	res := txResult{TxID: stub.txID, Err: err, Writes: stub.writes, Events: stub.events}
	if err == nil {
		for k, v := range stub.writes {
			l.committed[k] = v
		}
		res.Committed = true
	}
	return res
}

// query runs a read-only function and fails the test if it wrote anything.
func (l *fakeLedger) query(t *testing.T, msp string, fn func(ctx contractapi.TransactionContextInterface) (string, error)) (string, error) {
	t.Helper()
	var out string
	res := l.invoke(msp, func(ctx contractapi.TransactionContextInterface) error {
		var err error
		out, err = fn(ctx)
		return err
	})
	if len(res.Writes) != 0 {
		t.Fatalf("query wrote %d key(s); queries must be read-only", len(res.Writes))
	}
	return out, res.Err
}

// snapshotState copies committed state, for before/after comparisons.
func (l *fakeLedger) snapshotState() map[string]string {
	out := make(map[string]string, len(l.committed))
	for k, v := range l.committed {
		out[k] = string(v)
	}
	return out
}

// ---------------------------------------------------------------------------

type txStub struct {
	shim.ChaincodeStubInterface // nil: any method we did not implement panics

	ledger *fakeLedger
	txID   string
	ts     time.Time
	writes map[string][]byte
	events map[string][]byte
}

func (s *txStub) GetTxID() string { return s.txID }

func (s *txStub) GetTxTimestamp() (*timestamppb.Timestamp, error) { return timestamppb.New(s.ts), nil }

func (s *txStub) GetState(key string) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("empty key")
	}
	v, ok := s.ledger.committed[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (s *txStub) PutState(key string, value []byte) error {
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if value == nil {
		return fmt.Errorf("nil value for %q", key)
	}
	s.writes[key] = append([]byte(nil), value...)
	return nil
}

func (s *txStub) SetEvent(name string, payload []byte) error {
	if name == "" {
		return fmt.Errorf("empty event name")
	}
	// Fabric keeps only the last event set in a transaction.
	for k := range s.events {
		delete(s.events, k)
	}
	s.events[name] = payload
	return nil
}

func (s *txStub) CreateCompositeKey(objectType string, attributes []string) (string, error) {
	return shim.CreateCompositeKey(objectType, attributes)
}

func (s *txStub) SplitCompositeKey(compositeKey string) (string, []string, error) {
	if !strings.HasPrefix(compositeKey, "\x00") {
		return "", nil, fmt.Errorf("not a composite key: %q", compositeKey)
	}
	parts := strings.Split(compositeKey[1:], "\x00")
	// A composite key ends with a delimiter, so the last split element is "".
	if len(parts) < 2 || parts[len(parts)-1] != "" {
		return "", nil, fmt.Errorf("malformed composite key: %q", compositeKey)
	}
	parts = parts[:len(parts)-1]
	return parts[0], parts[1:], nil
}

func (s *txStub) GetStateByPartialCompositeKey(objectType string, keys []string) (shim.StateQueryIteratorInterface, error) {
	prefix, err := shim.CreateCompositeKey(objectType, keys)
	if err != nil {
		return nil, err
	}
	var ks []string
	for k := range s.ledger.committed {
		if strings.HasPrefix(k, prefix) {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	kvs := make([]*queryresult.KV, 0, len(ks))
	for _, k := range ks {
		kvs = append(kvs, &queryresult.KV{Key: k, Value: append([]byte(nil), s.ledger.committed[k]...)})
	}
	return &kvIterator{kvs: kvs}, nil
}

type kvIterator struct {
	kvs []*queryresult.KV
	i   int
}

func (it *kvIterator) HasNext() bool { return it.i < len(it.kvs) }
func (it *kvIterator) Close() error  { return nil }
func (it *kvIterator) Next() (*queryresult.KV, error) {
	if it.i >= len(it.kvs) {
		return nil, fmt.Errorf("iterator exhausted")
	}
	kv := it.kvs[it.i]
	it.i++
	return kv, nil
}

// fakeIdentity stands in for the identity Fabric authenticates from the
// signed proposal. Only the MSP ID matters to this chaincode.
type fakeIdentity struct{ msp string }

func (f fakeIdentity) GetID() (string, error)    { return "x509::CN=user@" + f.msp, nil }
func (f fakeIdentity) GetMSPID() (string, error) { return f.msp, nil }
func (f fakeIdentity) GetAttributeValue(string) (string, bool, error) {
	return "", false, nil
}
func (f fakeIdentity) AssertAttributeValue(name, _ string) error {
	return fmt.Errorf("attribute %s not present", name)
}
func (f fakeIdentity) GetX509Certificate() (*x509.Certificate, error) { return nil, nil }
