import type { NetLeg } from '../api/types'

/** Share of the gross that netting removes, as a whole percent (integer maths). */
export function reductionPct(gross: number, net: number): number {
  if (gross <= 0) return 0
  return Math.round(((gross - net) * 100) / gross)
}

/**
 * Total net amount that moves in one currency: the sum of every net leg. A
 * batch across more than two banks can have several legs per currency.
 */
export function netTotal(net: NetLeg[], ccy: string): number {
  return net.filter((x) => x.currency === ccy).reduce((sum, x) => sum + x.amount, 0)
}
