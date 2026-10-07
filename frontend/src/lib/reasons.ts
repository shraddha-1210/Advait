// One-line plain-English reading of each refusal code. The Security screen shows it next to the real
// code and message; Settlement and Netting lead with it and keep the code under "Technical details".
export const REASON: Record<string, string> = {
  ERR_BATCH: 'The batch is not valid: a trade is listed twice, or there are too few or too many trades.',
  ERR_GRIDLOCK: 'No set of these trades can be funded together, so nothing settles and nothing moves.',
  ERR_SINGLE_ORG_TRADE: 'Both sides of the trade are held by one bank org. Every trade must span both orgs.',
  ERR_INVALID_INPUT: 'The request is not valid. Clients send trade IDs only; the ledger computes every figure.',
  ERR_UNAUTHORIZED: 'Only a settlement bank may move value.',
  ERR_TRADE_NOT_FOUND: 'A trade in the request does not exist on the ledger.',
  ERR_INVARIANT_VIOLATION: 'The result would create or destroy value, so the ledger refused it.',

  ERR_ALREADY_SETTLED: 'A trade settles once. The ledger already records this trade as settled.',
  ERR_REPLAY: 'An old instruction cannot reopen a trade that has already settled.',
  ERR_INSUFFICIENT_FUNDS: 'The payer cannot fund its leg, so neither leg moves.',
  ENDORSEMENT_POLICY_FAILURE: "Only one bank's peer endorsed. The policy needs both banks, so the network invalidated it.",
  ERR_UNILATERAL: 'Only one bank instructed this trade. Settlement needs matching instructions from both.',
  ERR_FORGED_INSTRUCTION: "A bank tried to instruct on its counterparty's behalf. The instruction must come from the submitter.",
  ERR_INSTRUCTION_MISMATCH: "The two banks' instructions disagree, so there is no agreed trade to settle.",
  ERR_ATTESTATION_UNSIGNED: 'The rate carries no oracle signature.',
  ERR_ATTESTATION_BAD_SIGNATURE: 'The signature does not verify against the oracle key pinned at setup.',
  ERR_ATTESTATION_STALE: 'The rate is not newer than the latest one. Old rates cannot be replayed.',
  ERR_RATE_MISMATCH: 'The INR amount is not the USD amount at the signed rate.',
  ERR_INVALID_AMOUNT: 'Amounts must be positive integers in minor units.',
  ERR_AMOUNT_OVERFLOW: 'The amount is outside the allowed range, so it cannot wrap around.',
  ERR_ALREADY_INITIALIZED: "The ledger's configuration and money supply are set once and cannot be replaced.",
  ENDORSER_UNAVAILABLE: "A bank's peer could not endorse, so no valid transaction could be built. Nothing was submitted.",
  ENDORSE_FAILED: "Endorsement could not be collected from both banks' peers. Nothing was submitted.",
}

/** A plain sentence for a refusal code, for screens where codes stay in the details. */
export function plainReason(code?: string): string {
  return (code && REASON[code]) || 'The ledger refused the transaction.'
}
