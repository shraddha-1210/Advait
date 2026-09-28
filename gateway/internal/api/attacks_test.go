package api

import (
	"testing"

	"github.com/advait/pvp-settlement/gateway/internal/ledger"
)

func TestIsBankOfflineExpected(t *testing.T) {
	tests := []struct {
		name     string
		outcome  ledger.Outcome
		expected bool
	}{
		{
			name: "ENDORSER_UNAVAILABLE with endorse stage is accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "ENDORSER_UNAVAILABLE",
			},
			expected: true,
		},
		{
			name: "ENDORSE_FAILED with endorse stage is accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "ENDORSE_FAILED",
			},
			expected: true,
		},
		{
			name: "unknown error code with endorse stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "ERR_CUSTOM_REJECTION",
			},
			expected: false,
		},
		{
			name: "PROPOSAL_ERROR with endorse stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "PROPOSAL_ERROR",
			},
			expected: false,
		},
		{
			name: "chaincode rejection ERR_UNILATERAL with endorse stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "ERR_UNILATERAL",
			},
			expected: false,
		},
		{
			name: "chaincode rejection ERR_INSUFFICIENT_FUNDS with endorse stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "endorse",
				Code:  "ERR_INSUFFICIENT_FUNDS",
			},
			expected: false,
		},
		{
			name: "ENDORSER_UNAVAILABLE at commit stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "commit",
				Code:  "ENDORSER_UNAVAILABLE",
			},
			expected: false,
		},
		{
			name: "successful result with endorse stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    true,
				Stage: "endorse",
				Code:  "",
			},
			expected: false,
		},
		{
			name: "successful result without stage is NOT accepted",
			outcome: ledger.Outcome{
				OK:    true,
				Stage: "",
				Code:  "",
			},
			expected: false,
		},
		{
			name: "commit stage failure is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "commit",
				Code:  "ENDORSEMENT_POLICY_FAILURE",
			},
			expected: false,
		},
		{
			name: "submit stage failure is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "submit",
				Code:  "SUBMIT_FAILED",
			},
			expected: false,
		},
		{
			name: "connect stage failure is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "connect",
				Code:  "NO_IDENTITY",
			},
			expected: false,
		},
		{
			name: "empty stage failure is NOT accepted",
			outcome: ledger.Outcome{
				OK:    false,
				Stage: "",
				Code:  "UNKNOWN",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBankOfflineExpected(tt.outcome)
			if got != tt.expected {
				t.Errorf("isBankOfflineExpected(%+v) = %v, want %v", tt.outcome, got, tt.expected)
			}
		})
	}
}
