// Response and request shapes of the Advait gateway (gateway/internal/api).
//
// Every type below traces to a Go struct. Where the gateway passes chaincode
// JSON through untouched (rawJSON in server.go), the source is the chaincode
// struct in chaincode/pvp/contract. Go int64 values arrive as JSON numbers;
// every amount the chaincode accepts is capped at 10^15 minor units
// (money.go MaxAmountMinor), below Number.MAX_SAFE_INTEGER, so they are exact.

/** ledger.Party (gateway/internal/ledger/ledger.go). */
export type Party = 'BANKIN' | 'BANKFX'

/** Bank roles and currencies (chaincode/pvp/contract/model.go). */
export type Bank = 'BANKIN' | 'BANKFX'
export type Currency = 'INR' | 'USD'

/** Trade statuses (model.go StatusPendingMatch, StatusMatched, StatusSettled). */
export type TradeStatus = 'PENDING_MATCH' | 'MATCHED' | 'SETTLED'

/** Audit log types written by appendLog in contract.go. */
export type LogType = 'INIT' | 'RATE_PUBLISHED' | 'INSTRUCTED' | 'INSTRUCTION_WITHDRAWN' | 'SETTLED' | 'NET_SETTLED' | 'LIQUIDITY_SETTLED'

/** bank -> currency -> minor units. contract.go snapshot(). */
export type Balances = Record<string, Record<string, number>>

/** ledger.Outcome (ledger.go). */
export interface Outcome {
  ok: boolean
  txId?: string
  party: Party
  mspId: string
  function: string
  /** "endorse" | "commit" | "submit" | "connect" */
  stage?: string
  code?: string
  message?: string
  endorsingOrgs?: string[]
  blockNumber?: number
  result?: string
}

/** Anonymous conservation element of api.Snapshot (server.go). The gateway
 *  drops the chaincode's `accounts` field here. */
export interface SnapshotConservation {
  currency: string
  supply: number
  sum: number
  holds: boolean
}

/** api.Snapshot (server.go): a real GetBalances read. */
export interface Snapshot {
  balances: Balances
  conservation: SnapshotConservation[]
  invariantHolds: boolean
  readAt: string
}

/** api.WriteResult (server.go): outcome plus real before/after reads. */
export interface WriteResult {
  outcome: Outcome
  before: Snapshot | null
  after: Snapshot | null
  balancesMoved: boolean
}

/** contract.Config (model.go). */
export interface Config {
  banks: Record<string, string>
  auditorMsps: string[]
  pair: string
  oraclePublicKey: string
  oracleName: string
  rateWindow: number
}

/** GET /api/health (server.go health). 200 with ok=true, or 503 with ok=false. */
export interface Health {
  ok: boolean
  parties: Record<string, string>
  oracle: string
  config?: Config
  error?: string
}

/** attest.Attestation (chaincode/pvp/attest/attest.go). */
export interface Attestation {
  pair: string
  seq: number
  rateMicros: number
  source: string
  asOf: string
  signature: string
}

/** contract.RateRecord (model.go): Attestation embedded, plus relay fields. */
export interface RateRecord extends Attestation {
  publishedTx: string
  publishedBy: string
  verifiedWith: string
}

/** GetRates (contract.go). */
export interface Rates {
  head: number
  rateWindow: number
  rates: RateRecord[]
}

/** contract.Trade (model.go). */
export interface Trade {
  tradeId: string
  status: TradeStatus
  usdDeliverer: string
  inrDeliverer: string
  usdAmount: number
  inrAmount: number
  rateSeq: number
  rateMicros: number
  instructedBy: string[]
  settledTx?: string
  settledVia?: string
  balancesBefore?: Balances
  balancesAfter?: Balances
}

/** contract.Instruction (model.go). */
export interface Instruction {
  tradeId: string
  asBank: string
  usdDeliverer: string
  usdAmount: string
  inrAmount: string
  rateSeq: number
}

/** contract.StoredInstruction (model.go): Instruction embedded. */
export interface StoredInstruction extends Instruction {
  submitterMsp: string
  txId: string
}

/** GET /api/trades/{id} (server.go trade -> GetTrade in contract.go). 404 if unknown. */
export interface TradeDetail {
  trade: Trade
  /** keyed by bank: only the banks that have instructed */
  instructions: Record<string, StoredInstruction>
}

/** GET /api/state (server.go state). */
export interface State {
  snapshot: Snapshot
  rates: Rates
  trades: Trade[]
}

/** contract.LogEntry (model.go). */
export interface LogEntry {
  n: number
  type: LogType | string
  txId: string
  submitterMsp: string
  txTimestamp: string
  ref: string
  detail: string
}

/** contract.ConservationReport (model.go), as returned by CheckInvariant. */
export interface ConservationReport {
  currency: string
  supply: number
  sum: number
  holds: boolean
  accounts: number
}

/** CheckInvariant (contract.go). */
export interface Invariant {
  holds: boolean
  currencies: ConservationReport[]
}

/** GET /api/audit (server.go audit). */
export interface Audit {
  queriedAs: string
  log: LogEntry[]
  trades: Trade[]
  rates: Rates
  invariant: Invariant
}

/** GET /api/quote (QuoteINR in contract.go). */
export interface Quote {
  usdAmount: number
  rateSeq: number
  rateMicros: number
  inrAmount: number
}

/** api.Rejection (server.go). */
export interface Rejection {
  at: string
  attack?: string
  outcome: Outcome
  attempt: string
}

/** GET /api/rejections (server.go rejections). */
export interface Rejections {
  note: string
  /** null until the first refusal: Go encodes the gateway's nil slice as null */
  rejections: Rejection[] | null
}

/** api.AttackInfo (attacks.go). */
export interface AttackInfo {
  name: string
  threat: string
  actor: string
  description: string
  /** expected rejection code */
  expect: string
  /** where the network refuses it */
  stage: string
}

/** api.AttackReport (attacks.go): AttackInfo embedded. */
export interface AttackReport extends AttackInfo {
  tradeId?: string
  /** null when the attack needed no setup (nil slice in Go) */
  setup: Outcome[] | null
  attempt: string
  payload: string[]
  result: WriteResult | null
  refused: boolean
  expectedCode: boolean
  note?: string
}

/** POST /api/oracle/rates request body (server.go publishRate). */
export interface PublishRateBody {
  rateMicros: number
}

/** POST /api/oracle/rates response. */
export interface PublishRateResponse {
  attestation: Attestation
  result: WriteResult
}

/** api.InstructionBody (server.go). asBank is ignored by this endpoint. */
export interface InstructionBody {
  as: Party
  tradeId: string
  usdDeliverer: string
  /** cents, plain integer string */
  usdAmount: string
  /** paise, plain integer string */
  inrAmount: string
  rateSeq: number
}

/** POST /api/trades/{id}/settle request body. */
export interface SettleBody {
  as: Party
}

/** contract.NetLeg (netting.go). amount 0 = nets to zero, nothing moves. */
export interface NetLeg {
  currency: string
  from?: string
  to?: string
  amount: number
}

/** contract.NetPlan (netting.go). */
export interface NetPlan {
  batchId: string
  tradeIds: string[]
  /** total per currency if every trade settled one by one */
  gross: Record<string, number>
  /** bank -> currency -> amount that bank would pay gross */
  grossByPayer: Record<string, Record<string, number>>
  /** one entry per currency */
  net: NetLeg[]
}

/** POST /api/net-preview (chaincode PreviewNet). 422 with {error} if the chaincode refuses the batch. */
export interface NetPreview {
  plan: NetPlan
  fundsOk: boolean
  fundsMessage: string
}

/** contract.Batch (netting.go): the on-ledger record of a settled batch. */
export interface Batch extends NetPlan {
  settledTx: string
  submittedBy: string
  balancesBefore: Balances
  balancesAfter: Balances
}

/** api.NetBody (gateway netting.go). */
export interface NetBody {
  as?: Party
  batchId?: string
  tradeIds: string[]
}

/** api.NetSettleResponse (gateway netting.go). */
export interface NetSettleResponse {
  batchId: string
  result: WriteResult
  /** present only when the batch committed (read back with GetBatch) */
  batch?: Batch
}

/** writeErr (server.go): every non-2xx response except /api/health. */
export interface GatewayError {
  error: string
}

/** contract.Removal (liquidity.go): one gridlock-resolution step. */
export interface Removal {
  step: number
  tradeId: string
  /** the short bank, and the currency it was short in */
  bank: string
  currency: string
  /** net outflow minus balance, before this removal (minor units) */
  shortfall: number
  /** the removed trade's leg that bank would have paid (minor units) */
  amount: number
}

/** contract.Cycle (netting.go): a circular chain of obligations in one currency. */
export interface Cycle {
  currency: string
  /** banks in order, first repeated at the end */
  path: string[]
  /** amount offset around the cycle */
  bottleneck: number
}

/** contract.LiquidityPlan (liquidity.go). Computed by the chaincode from ledger state. */
export interface LiquidityPlan {
  batchId: string
  inputTradeIds: string[]
  settledTradeIds: string[]
  droppedTradeIds: string[]
  removals: Removal[]
  cycles: Cycle[]
  /** absent when gridlocked */
  netPlan?: NetPlan
  gridlocked: boolean
}

/** POST /api/liquidity/preview (chaincode PreviewLiquidity). 422 with {error} if a trade is invalid. */
export interface LiquidityPreview {
  plan: LiquidityPlan
  fundsOk: boolean
  fundsMessage: string
}

/** POST /api/liquidity/resolve: every MATCHED trade (up to 50) and the chaincode's preview of them. */
export interface LiquidityResolve {
  batchId: string
  tradeIds: string[]
  matched: number
  truncated: boolean
  preview: LiquidityPreview | null
}

/** contract.LiquidityDetail, stored on a liquidity batch record. */
export interface LiquidityDetail {
  inputTradeIds: string[]
  droppedTradeIds: string[]
  removals: Removal[]
  cycles: Cycle[]
}

/** api.LiquiditySettleResponse (gateway liquidity.go). */
export interface LiquiditySettleResponse {
  batchId: string
  result: WriteResult
  /** present only when the batch committed (read back with GetBatch) */
  batch?: Batch & { liquidity?: LiquidityDetail }
}

/** GET /api/liquidity/scenarios: seed groupings only, no figures. */
export interface LiquidityScenario {
  id: string
  title: string
  note: string
  tradeIds: string[]
}
