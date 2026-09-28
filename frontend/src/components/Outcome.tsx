import type { ReactNode } from 'react'
import type { Balances, WriteResult } from '../api/types'
import { cx } from '../lib/cx'
import { bankLabel, money } from '../lib/format'
import { plainReason } from '../lib/reasons'
import { IconCheck, IconX } from './Icons'

const BANKS = ['BANKIN', 'BANKFX'] as const
const CCYS = ['INR', 'USD'] as const

export type Reread = 'pending' | 'unchanged' | 'changed' | 'failed'

export function Meta({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="eyebrow">{k}</dt>
      <dd className="mt-1 text-sm font-medium">{v}</dd>
    </div>
  )
}

/** Green outcome, in plain words, with the real before/after reads as proof. */
export function SettledPanel({ result, headline, children }: { result: WriteResult; headline: string; children?: ReactNode }) {
  const o = result.outcome
  return (
    <div className="anim-rise mt-6 rounded-2xl bg-ok-soft p-6 ring-1 ring-inset ring-ok-line">
      <p className="flex items-center gap-2.5 text-lg font-semibold text-ok">
        <IconCheck size={20} /> {headline}
      </p>
      {children}
      <BeforeAfter before={result.before?.balances} after={result.after?.balances} />
      <details className="mt-4 text-sm">
        <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Technical details</summary>
        <dl className="mt-3 grid gap-x-8 gap-y-3 sm:grid-cols-3">
          <Meta k="Ledger transaction" v={<span className="block truncate font-mono">{o.txId}</span>} />
          <Meta k="Block" v={<span className="tnum">{o.blockNumber ?? 'n/a'}</span>} />
          <Meta k="Submitted by" v={`${bankLabel(o.party)} (${o.mspId})`} />
        </dl>
      </details>
    </div>
  )
}

/**
 * Red outcome, in plain words. The refusal code and the chaincode's message
 * are real and are kept, one click away, under "Technical details".
 */
export function RejectedPanel({ result, reread, headline }: { result: WriteResult; reread: Reread; headline: string }) {
  const o = result.outcome
  return (
    <div className="anim-rise mt-6 rounded-2xl bg-bad-soft p-6 ring-1 ring-inset ring-bad-line">
      <p className="flex items-center gap-2.5 text-lg font-semibold text-bad">
        <IconX size={20} /> {headline}
      </p>
      <p className="mt-2 text-[15px] text-fg">{plainReason(o.code)}</p>
      <p className="mt-3 text-sm">
        {reread === 'pending' ? (
          <span className="text-muted">Re-reading balances from the ledger…</span>
        ) : reread === 'unchanged' ? (
          <span className="inline-flex items-center gap-1.5 font-semibold text-ok">
            <IconCheck size={15} /> Balances re-read from the ledger: unchanged.
          </span>
        ) : reread === 'changed' ? (
          <span className="font-semibold text-bad">Balances re-read differ: another transaction may have committed meanwhile.</span>
        ) : (
          <span className="text-bad">Could not re-read balances.</span>
        )}
      </p>
      <details className="mt-4 text-sm">
        <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Technical details</summary>
        <p className="mt-3 break-words font-mono font-semibold text-bad">{o.code}</p>
        <p className="mt-1.5 break-words rounded-xl bg-surface/80 p-3 font-mono text-[13px] leading-relaxed text-fg-2 ring-1 ring-inset ring-line">
          {o.message}
        </p>
        <dl className="mt-3 grid gap-x-8 gap-y-3 sm:grid-cols-3">
          <Meta k="Refused at" v={o.stage ?? 'n/a'} />
          <Meta k="Proposal" v={<span className="block truncate font-mono">{o.txId ?? 'n/a'}</span>} />
          <Meta k="Gateway before/after reads" v={result.balancesMoved ? 'Differ' : 'Identical'} />
        </dl>
      </details>
    </div>
  )
}

export function BeforeAfter({ before, after }: { before?: Balances; after?: Balances }) {
  if (!before || !after) return null
  return (
    <div className="mt-5 overflow-x-auto rounded-xl bg-surface/80 p-4 ring-1 ring-inset ring-line">
      <table className="w-full min-w-md text-left text-sm">
        <caption className="eyebrow mb-3 text-left">Balances read from the ledger, before and after</caption>
        <thead className="eyebrow">
          <tr>
            <th className="pb-2 pr-4 font-semibold">Account</th>
            <th className="pb-2 pr-4 text-right font-semibold">Before</th>
            <th className="pb-2 pr-4 text-right font-semibold">After</th>
            <th className="pb-2 text-right font-semibold">Change</th>
          </tr>
        </thead>
        <tbody className="tnum">
          {BANKS.flatMap((b) =>
            CCYS.map((c) => {
              const d = (after[b]?.[c] ?? 0) - (before[b]?.[c] ?? 0)
              return (
                <tr key={b + c} className="border-t border-line">
                  <td className="py-2 pr-4 font-medium">
                    {bankLabel(b)} {c}
                  </td>
                  <td className="py-2 pr-4 text-right text-muted">{money(before[b]?.[c] ?? 0, c)}</td>
                  <td className="py-2 pr-4 text-right">{money(after[b]?.[c] ?? 0, c)}</td>
                  <td className={cx('py-2 text-right font-semibold', d > 0 ? 'text-ok' : d < 0 ? 'text-bad' : 'text-muted')}>
                    {d > 0 ? '+' : ''}
                    {d === 0 ? '0' : money(d, c)}
                  </td>
                </tr>
              )
            }),
          )}
        </tbody>
      </table>
    </div>
  )
}
