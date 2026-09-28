import { cx } from '../lib/cx'

/** Labelled switch that hides (never deletes) trades created by the test suites. */
export function TestTradeToggle({ hide, onChange, hidden }: { hide: boolean; onChange: (v: boolean) => void; hidden: number }) {
  return (
    <label className="flex cursor-pointer select-none items-center gap-2 text-[13px] font-medium text-muted hover:text-fg">
      <input type="checkbox" className="peer sr-only" checked={hide} onChange={(e) => onChange(e.target.checked)} />
      <span
        className={cx(
          'relative h-4 w-7 shrink-0 rounded-full transition-colors peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-[var(--focus)]',
          hide ? 'bg-accent' : 'bg-surface-3 ring-1 ring-inset ring-line-strong',
        )}
        aria-hidden="true"
      >
        <span className={cx('absolute top-0.5 h-3 w-3 rounded-full bg-white shadow transition-transform', hide ? 'translate-x-3.5' : 'translate-x-0.5')} />
      </span>
      Hide test-suite trades{hide && hidden > 0 ? ` (${hidden})` : ''}
    </label>
  )
}
