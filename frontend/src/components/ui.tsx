import type { ReactNode } from 'react'
import { cx } from '../lib/cx'
import { IconAlert, IconCheck, IconClock, IconX } from './Icons'

export function Card({
  children,
  className,
  title,
  subtitle,
  action,
  as: As = 'section',
  labelledBy,
}: {
  children: ReactNode
  className?: string
  /** Small uppercase label. */
  title?: ReactNode
  subtitle?: ReactNode
  action?: ReactNode
  as?: 'section' | 'div'
  labelledBy?: string
}) {
  return (
    <As className={cx('card p-6 lg:p-7', className)} aria-labelledby={labelledBy}>
      {(title || action) && (
        <div className="mb-6 flex items-start justify-between gap-4">
          <div className="min-w-0">
            {title && (
              <h2 id={labelledBy} className="eyebrow">
                {title}
              </h2>
            )}
            {subtitle && <p className="mt-1.5 text-[15px] font-medium text-fg-2">{subtitle}</p>}
          </div>
          {action}
        </div>
      )}
      {children}
    </As>
  )
}

export type Tone = 'ok' | 'bad' | 'warn' | 'neutral' | 'accent'

const toneClass: Record<Tone, string> = {
  ok: 'bg-ok-soft text-ok ring-ok-line',
  bad: 'bg-bad-soft text-bad ring-bad-line',
  warn: 'bg-warn-soft text-warn ring-warn-line',
  neutral: 'bg-surface-2 text-muted ring-line-strong',
  accent: 'bg-accent-soft text-accent-fg ring-accent/25',
}

/** Status chip: tint + hairline ring + icon + label. Never colour alone. */
export function Chip({ tone = 'neutral', icon, children, className }: {
  tone?: Tone
  icon?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <span
      className={cx(
        'inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 text-xs font-semibold ring-1 ring-inset',
        toneClass[tone],
        className,
      )}
    >
      {icon}
      {children}
    </span>
  )
}

export function StatusChip({ status }: { status: string }) {
  if (status === 'SETTLED') return <Chip tone="ok" icon={<IconCheck size={13} />}>Settled</Chip>
  if (status === 'MATCHED') return <Chip tone="neutral" icon={<IconCheck size={13} />}>Matched</Chip>
  if (status === 'PENDING_MATCH') return <Chip tone="warn" icon={<IconClock size={13} />}>Pending match</Chip>
  return <Chip>{status}</Chip>
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cx('skeleton', className)} aria-hidden="true" />
}

export function ErrorBox({ title, message, onRetry }: { title: string; message: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="flex items-start gap-3 rounded-2xl bg-bad-soft p-4 text-bad ring-1 ring-inset ring-bad-line">
      <IconAlert size={18} className="mt-0.5 shrink-0" />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-semibold">{title}</p>
        <p className="mt-1 break-words font-mono text-xs leading-relaxed">{message}</p>
      </div>
      {onRetry && (
        <button onClick={onRetry} className="rounded-lg px-3 py-1.5 text-sm font-semibold ring-1 ring-inset ring-bad-line hover:bg-bad/10">
          Retry
        </button>
      )}
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="well px-6 py-10 text-center text-sm leading-relaxed text-muted">{children}</div>
}

export function Button({
  children,
  onClick,
  disabled,
  loading,
  variant = 'primary',
  size = 'md',
  type = 'button',
  className,
  ariaLabel,
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  loading?: boolean
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost'
  size?: 'sm' | 'md' | 'lg'
  type?: 'button' | 'submit'
  className?: string
  ariaLabel?: string
}) {
  const base =
    'inline-flex items-center justify-center gap-2 rounded-xl font-semibold transition-all duration-150 active:translate-y-px disabled:cursor-not-allowed disabled:opacity-45 disabled:shadow-none disabled:filter-none'
  const v = {
    primary: 'btn-primary',
    secondary: 'bg-surface text-fg shadow-[0_1px_2px_rgba(17,24,56,0.06)] ring-1 ring-inset ring-line-strong hover:bg-surface-2',
    danger: 'bg-bad-soft text-bad ring-1 ring-inset ring-bad-line hover:bg-bad/15',
    ghost: 'text-muted hover:bg-surface-2 hover:text-fg',
  }[variant]
  const s = { sm: 'h-8 px-3 text-sm', md: 'h-10 px-4 text-sm', lg: 'h-12 px-7 text-[15px]' }[size]
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      aria-label={ariaLabel}
      className={cx(base, v, s, className)}
    >
      {children}
    </button>
  )
}

/** Inline result line for a write: shows the real outcome, never a guess. */
export function OutcomeLine({ ok, code, message, stage }: { ok: boolean; code?: string; message?: string; stage?: string }) {
  return ok ? (
    <p className="flex items-start gap-2 text-sm font-medium text-ok">
      <IconCheck size={16} className="mt-0.5 shrink-0" />
      <span>Committed</span>
    </p>
  ) : (
    <p className="flex items-start gap-2 text-sm text-bad">
      <IconX size={16} className="mt-0.5 shrink-0" />
      <span className="min-w-0 break-words">
        <span className="font-semibold">Refused{stage ? ` at ${stage}` : ''}: </span>
        <span className="font-mono">{code}</span>
        {message ? <span className="block font-mono text-xs opacity-90">{message}</span> : null}
      </span>
    </p>
  )
}
