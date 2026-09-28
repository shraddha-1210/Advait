// Every call to the gateway goes through this file. There is no mock layer:
// if the gateway is down, calls fail and the UI says so.
import type {
  AttackInfo,
  AttackReport,
  Audit,
  Health,
  InstructionBody,
  NetPreview,
  NetSettleResponse,
  Party,
  PublishRateResponse,
  Quote,
  Rejections,
  State,
  TradeDetail,
  WriteResult,
} from './types'

export const GATEWAY_URL: string = (import.meta.env.VITE_GATEWAY_URL as string | undefined) ?? 'http://localhost:8080'

/**
 * A call either returns the gateway's data, or fails. Failures keep the
 * gateway's own text: `{"error": ...}` bodies (writeErr in server.go) are
 * surfaced verbatim. A chaincode refusal is NOT a failure here: the gateway
 * returns 200 with `outcome.ok = false`, and callers show that outcome.
 */
export type ApiResult<T> =
  | { ok: true; data: T; status: number }
  | { ok: false; status: number; error: string; kind: 'http' | 'network' }

async function request<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<ApiResult<T>> {
  let res: Response
  try {
    res = await fetch(GATEWAY_URL + path, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (e) {
    return { ok: false, status: 0, kind: 'network', error: `Cannot reach gateway at ${GATEWAY_URL}: ${(e as Error).message}` }
  }
  const text = await res.text()
  let parsed: unknown = undefined
  try {
    parsed = text ? JSON.parse(text) : undefined
  } catch {
    // not JSON; fall through with the raw text
  }
  if (!res.ok) {
    const msg =
      parsed && typeof parsed === 'object' && 'error' in parsed ? String((parsed as { error: unknown }).error) : text || res.statusText
    return { ok: false, status: res.status, kind: 'http', error: msg }
  }
  return { ok: true, status: res.status, data: parsed as T }
}

/** GET /api/health. A 503 still carries a Health body (ok=false, error). */
export async function getHealth(): Promise<ApiResult<Health>> {
  let res: Response
  try {
    res = await fetch(GATEWAY_URL + '/api/health')
  } catch (e) {
    return { ok: false, status: 0, kind: 'network', error: `Cannot reach gateway at ${GATEWAY_URL}: ${(e as Error).message}` }
  }
  try {
    const h = (await res.json()) as Health
    if (!h.ok) return { ok: false, status: res.status, kind: 'http', error: h.error ?? `health returned ${res.status}` }
    return { ok: true, status: res.status, data: h }
  } catch {
    return { ok: false, status: res.status, kind: 'http', error: `health returned ${res.status} with a non-JSON body` }
  }
}

export const getState = () => request<State>('GET', '/api/state')
export const getAudit = () => request<Audit>('GET', '/api/audit')
export const getAttacks = () => request<AttackInfo[]>('GET', '/api/attacks')
export const getRejections = () => request<Rejections>('GET', '/api/rejections')

/** Point read of one trade (chaincode GetTrade). 404 if unknown. */
export const getTrade = (id: string) => request<TradeDetail>('GET', `/api/trades/${encodeURIComponent(id)}`)

export const getQuote = (usdCents: string, seq: number) =>
  request<Quote>('GET', `/api/quote?usd=${encodeURIComponent(usdCents)}&seq=${seq}`)

export const publishRate = (rateMicros: number) =>
  request<PublishRateResponse>('POST', '/api/oracle/rates', { rateMicros })

export const submitInstruction = (body: InstructionBody) => request<WriteResult>('POST', '/api/instructions', body)

export const settleTrade = (tradeId: string, as: Party) =>
  request<WriteResult>('POST', `/api/trades/${encodeURIComponent(tradeId)}/settle`, { as })

/** Gross vs net for a batch, computed by the chaincode (read-only). */
export const previewNet = (tradeIds: string[]) => request<NetPreview>('POST', '/api/net-preview', { tradeIds })

/** Settle a batch as one net movement per currency, in one transaction. */
export const netSettle = (tradeIds: string[], as: Party) => request<NetSettleResponse>('POST', '/api/net-settle', { tradeIds, as })

export const runAttack = (name: string) => request<AttackReport>('POST', `/api/attacks/${encodeURIComponent(name)}`)
