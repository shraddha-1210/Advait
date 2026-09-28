import { cx } from '../lib/cx'
import { splitMinor } from '../lib/format'

const SIZES = {
  sm: { major: 'text-sm font-semibold', minor: 'text-sm', code: 'text-[10px]' },
  md: { major: 'text-xl font-semibold tracking-tight', minor: 'text-base', code: 'text-[11px]' },
  lg: { major: 'text-[32px] leading-none font-semibold tracking-[-0.02em]', minor: 'text-xl', code: 'text-xs' },
  hero: { major: 'text-[44px] xl:text-[56px] leading-none font-semibold tracking-[-0.035em]', minor: 'text-2xl xl:text-3xl', code: 'text-xs' },
} as const

/** A money figure from integer minor units: large major part, quiet minor part, tracked currency code. */
export function Money({ minor, ccy, size = 'md', className }: {
  minor: number
  ccy: string
  size?: keyof typeof SIZES
  className?: string
}) {
  const p = splitMinor(minor, ccy)
  const s = SIZES[size]
  return (
    <span className={cx('tnum inline-flex items-baseline gap-2', className)}>
      <span className={s.major}>
        {p.sign}
        {p.major}
        <span className={cx(s.minor, 'font-medium text-muted')}>.{p.minor}</span>
      </span>
      <span className={cx(s.code, 'font-semibold uppercase tracking-[0.14em] text-muted')}>{ccy}</span>
    </span>
  )
}
