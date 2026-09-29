// Money arrives as integer minor units (paise, cents). We never divide into a
// float: the major and minor parts are split with integer arithmetic, which is
// exact for every amount the chaincode accepts (<= 10^15).

const grouping: Record<string, string> = { INR: 'en-IN', USD: 'en-US' }

function group(n: number, ccy: string): string {
  return n.toLocaleString(grouping[ccy] ?? 'en-US', { maximumFractionDigits: 0 })
}

/** 12345678 paise, 'INR' -> { major: '1,23,456', minor: '78' } */
export function splitMinor(minor: number, ccy: string): { sign: string; major: string; minor: string } {
  const sign = minor < 0 ? '-' : ''
  const abs = Math.abs(minor)
  const major = Math.trunc(abs / 100)
  const rem = abs - major * 100
  return { sign, major: group(major, ccy), minor: String(rem).padStart(2, '0') }
}

/** 12345678, 'INR' -> '1,23,456.78 INR' */
export function money(minor: number, ccy: string): string {
  const p = splitMinor(minor, ccy)
  return `${p.sign}${p.major}.${p.minor} ${ccy}`
}

/** 83250000 micros -> '83.250000' (integer split, no float). */
export function rateFromMicros(micros: number): string {
  const whole = Math.trunc(micros / 1_000_000)
  const frac = micros - whole * 1_000_000
  return `${whole}.${String(frac).padStart(6, '0')}`
}

/**
 * Parse a user-typed decimal ('10000', '10,000.5') into integer minor units
 * with `places` decimals. Returns null if it is not a plain positive decimal.
 * String based, so no float rounding.
 */
export function parseDecimalToMinor(input: string, places: number): string | null {
  const s = input.replace(/[,\s_]/g, '')
  const m = /^(\d+)(?:\.(\d*))?$/.exec(s)
  if (!m) return null
  const frac = (m[2] ?? '').padEnd(places, '0')
  if (frac.length > places) return null
  const digits = (m[1] + frac).replace(/^0+(?=\d)/, '')
  if (/^0+$/.test(digits)) return null
  return digits
}

export function shortHash(h: string | undefined, n = 10): string {
  if (!h) return ''
  return h.length <= n + 2 ? h : `${h.slice(0, n)}…`
}

export function bankLabel(b: string): string {
  if (b === 'BANKIN') return 'BankIN'
  if (b === 'BANKFX') return 'BankFX'
  if (b === 'ORACLE') return 'Oracle'
  if (b === 'AUDITOR') return 'Auditor'
  return b
}

export function timeOf(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}
