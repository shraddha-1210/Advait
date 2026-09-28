import type { Trade } from '../api/types'

/**
 * Trades created by the automated test suites (IT- integration tests, ATK-
 * attack scenarios) and by manual testing (T- IDs) are real ledger records,
 * but they clutter a demo. The UI can hide them behind a labelled toggle; it
 * never removes or alters them. Trades created from the UI use USDINR- IDs.
 */
export function isTestTrade(id: string): boolean {
  return /^(ATK-|IT-|T-)/.test(id)
}

/** Non-test trades first, each group in ID order. */
export function demoFirst(trades: Trade[]): Trade[] {
  return [...trades].sort((a, b) => Number(isTestTrade(a.tradeId)) - Number(isTestTrade(b.tradeId)) || a.tradeId.localeCompare(b.tradeId))
}
