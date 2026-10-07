import { useMemo, useState, type ReactNode } from 'react'
import { getAudit } from '../api/client'
import { IconAlert, IconCheck, IconRefresh, IconX } from '../components/Icons'
import { Button, Chip, Empty, ErrorBox, Skeleton } from '../components/ui'
import { cx, inputClass } from '../lib/cx'
import { money, rateFromMicros, shortHash, timeOf } from '../lib/format'
import { useApi } from '../lib/useApi'

const TYPE_LABEL: Record<string, string> = {
  INIT: 'Init',
  RATE_PUBLISHED: 'Rate published',
  INSTRUCTED: 'Instructed',
  SETTLED: 'Settled',
  NET_SETTLED: 'Net settled',
  LIQUIDITY_SETTLED: 'Liquidity settled',
  INSTRUCTION_WITHDRAWN: 'Instruction withdrawn',
}

// The regulator's view: read-only, calmer and denser than the interactive
// screens. Every row is a chaincode read through GET /api/audit.
export function Audit({ online }: { online: boolean }) {
  const audit = useApi(getAudit)
  const [filter, setFilter] = useState<string>('ALL')
  const [query, setQuery] = useState('')

  // `n` is the append order; sort by it rather than trusting response order.
  const log = useMemo(() => (audit.data?.log ?? []).slice().sort((x, y) => x.n - y.n), [audit.data])
  const types = useMemo(() => {
    const c = new Map<string, number>()
    for (const e of log) c.set(e.type, (c.get(e.type) ?? 0) + 1)
    return c
  }, [log])
  const q = query.trim().toLowerCase()
  const shown = log.filter(
    (e) =>
      (filter === 'ALL' || e.type === filter) &&
      (!q || e.ref.toLowerCase().includes(q) || e.txId.toLowerCase().includes(q) || e.detail.toLowerCase().includes(q)),
  )
  const a = audit.data

  return (
    <div className="grid grid-cols-12 gap-6">
      {online && audit.error && !audit.loading && (
        <div className="col-span-12">
          <ErrorBox title="Could not read the audit view (GET /api/audit)" message={audit.error} onRetry={() => void audit.reload()} />
        </div>
      )}

      <section aria-labelledby="audit-title" className="card col-span-12 p-8">
        <div className="flex flex-wrap items-start justify-between gap-8">
          <div className="min-w-0 max-w-lg flex-1">
            <p className="eyebrow">Regulator view · read only</p>
            <h2 id="audit-title" className="mt-2 text-balance text-[26px] font-semibold leading-tight tracking-[-0.02em]">
              Append-only settlement record
            </h2>
            <p className="mt-3 text-[14px] leading-relaxed text-muted">
              Every instruction, rate and settlement, in the order the ledger recorded it, with transaction IDs and FX
              provenance. Timestamps are proposal times, for display only.
            </p>
            <p className="mt-3 text-xs text-muted">
              {a ? (
                <>
                  Queried as <span className="font-mono text-fg-2">{a.queriedAs}</span>: the Auditor's own read-only MSP on the channel. It has no peer, so it reads through a bank's peer.
                </>
              ) : (
                <Skeleton className="h-3 w-72" />
              )}
            </p>
          </div>
          <dl className="grid shrink-0 grid-cols-2 gap-3 sm:grid-cols-4">
            <Fact label="Entries" value={a?.log.length} />
            <Fact label="Trades" value={a?.trades.length} />
            <Fact label="Rates" value={a?.rates.rates.length} />
            <Fact
              label="Invariant"
              value={
                a ? (
                  a.invariant.holds ? (
                    <span className="inline-flex items-center gap-1.5 text-ok">
                      <IconCheck size={18} /> Holds
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1.5 text-bad">
                      <IconX size={18} /> Broken
                    </span>
                  )
                ) : undefined
              }
            />
          </dl>
        </div>
      </section>

      <section aria-labelledby="log-title" className="card col-span-12 overflow-hidden">
        <div className="flex flex-wrap items-center gap-4 border-b border-line px-7 py-5">
          <div className="mr-auto">
            <h2 id="log-title" className="eyebrow">
              Audit log
            </h2>
            <p className="mt-1 text-[15px] font-medium text-fg-2">Oldest first, by sequence number</p>
          </div>
          <div className="inline-flex h-9 items-center gap-1 rounded-xl bg-surface-3/70 p-1 ring-1 ring-inset ring-line" role="group" aria-label="Filter by entry type">
            {['ALL', 'INIT', 'RATE_PUBLISHED', 'INSTRUCTED', 'SETTLED', 'NET_SETTLED', 'LIQUIDITY_SETTLED'].map((t) => (
              <button
                key={t}
                onClick={() => setFilter(t)}
                aria-pressed={filter === t}
                disabled={!a}
                className={cx(
                  'flex h-7 items-center gap-1.5 rounded-lg px-3 text-[13px] font-semibold transition-all disabled:opacity-50',
                  filter === t ? 'card !rounded-lg text-fg' : 'text-muted hover:text-fg',
                )}
              >
                {t === 'ALL' ? 'All' : TYPE_LABEL[t]}
                <span className="tnum text-muted">{a ? (t === 'ALL' ? log.length : (types.get(t) ?? 0)) : '–'}</span>
              </button>
            ))}
          </div>
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search ref or tx ID"
            aria-label="Search by reference or transaction ID"
            className={cx(inputClass, '!h-9 w-52 font-mono text-xs')}
          />
          <Button variant="secondary" size="sm" onClick={() => void audit.reload()} loading={audit.refreshing && !!a} ariaLabel="Re-read audit log">
            <IconRefresh size={14} />
          </Button>
        </div>
        {a && <div className="px-7 pt-5 empty:hidden"><Completeness returned={log.length} maxN={log.length ? log[log.length - 1].n : 0} /></div>}
        {!a ? (
          <RowsSkeleton rows={8} />
        ) : shown.length === 0 ? (
          <div className="p-7">
            <Empty>No entries match.</Empty>
          </div>
        ) : (
          <div className="scroll-thin max-h-[36rem] overflow-auto">
            <table className="w-full min-w-[64rem] text-left text-[13px] leading-5">
              <thead className="sticky top-0 z-[1] bg-surface">
                <tr className="eyebrow border-b border-line">
                  <th className="px-7 py-3 font-semibold">#</th>
                  <th className="px-4 py-3 font-semibold">Type</th>
                  <th className="px-4 py-3 font-semibold">Ref</th>
                  <th className="px-4 py-3 font-semibold">Submitter</th>
                  <th className="px-4 py-3 font-semibold">Proposal time</th>
                  <th className="px-4 py-3 font-semibold">Detail</th>
                  <th className="px-7 py-3 font-semibold">Tx ID</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((e) => (
                  <tr key={e.n} className="border-b border-line align-top transition-colors last:border-0 hover:bg-surface-2">
                    <td className="tnum px-7 py-2.5 text-muted">{e.n}</td>
                    <td className="px-4 py-2.5">
                      <TypeTag type={e.type} />
                    </td>
                    <td className="px-4 py-2.5 font-mono">{e.ref}</td>
                    <td className="px-4 py-2.5 text-fg-2">{e.submitterMsp}</td>
                    <td className="tnum whitespace-nowrap px-4 py-2.5 text-fg-2">{timeOf(e.txTimestamp)}</td>
                    <td className="px-4 py-2.5 text-muted">{e.detail}</td>
                    <td className="px-7 py-2.5 font-mono text-muted" title={e.txId}>
                      {shortHash(e.txId, 14)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section aria-labelledby="prov-title" className="card col-span-12 overflow-hidden xl:col-span-8">
        <div className="border-b border-line px-7 py-5">
          <h2 id="prov-title" className="eyebrow">
            FX rate provenance
          </h2>
          <p className="mt-1 text-[15px] font-medium text-fg-2">Every signed attestation the chaincode accepted</p>
        </div>
        {!a ? (
          <RowsSkeleton rows={3} />
        ) : a.rates.rates.length === 0 ? (
          <div className="p-7">
            <Empty>No rates published.</Empty>
          </div>
        ) : (
          <div className="scroll-thin max-h-80 overflow-auto">
            <table className="w-full min-w-[46rem] text-left text-[13px] leading-5">
              <thead className="sticky top-0 z-[1] bg-surface">
                <tr className="eyebrow border-b border-line">
                  <th className="px-7 py-3 font-semibold">Seq</th>
                  <th className="px-4 py-3 text-right font-semibold">Rate</th>
                  <th className="px-4 py-3 font-semibold">Source</th>
                  <th className="px-4 py-3 font-semibold">As of</th>
                  <th className="px-4 py-3 font-semibold">Verified with key</th>
                  <th className="px-7 py-3 font-semibold">Published tx</th>
                </tr>
              </thead>
              <tbody>
                {a.rates.rates
                  .slice()
                  .reverse()
                  .map((r) => (
                    <tr key={r.seq} className="border-b border-line last:border-0 hover:bg-surface-2">
                      <td className="tnum px-7 py-2.5">
                        {r.seq}
                        {r.seq === a.rates.head && (
                          <Chip tone="accent" className="ml-2 !h-5 !px-2 text-[10px]">
                            Latest
                          </Chip>
                        )}
                      </td>
                      <td className="tnum px-4 py-2.5 text-right font-semibold">
                        {rateFromMicros(r.rateMicros)} <span className="font-normal text-muted">{r.pair}</span>
                      </td>
                      <td className="px-4 py-2.5 text-fg-2">{r.source}</td>
                      <td className="tnum whitespace-nowrap px-4 py-2.5 text-fg-2">{timeOf(r.asOf)}</td>
                      <td className="px-4 py-2.5 font-mono text-muted" title={r.verifiedWith}>
                        {shortHash(r.verifiedWith, 14)}
                      </td>
                      <td className="px-7 py-2.5 font-mono text-muted" title={r.publishedTx}>
                        {shortHash(r.publishedTx, 14)}
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section aria-labelledby="inv-title" className="card col-span-12 p-7 xl:col-span-4">
        <h2 id="inv-title" className="eyebrow">
          Invariant check
        </h2>
        <p className="mt-1 text-[15px] font-medium text-fg-2">Recomputed from every balance on the ledger</p>
        <div className="mt-6 space-y-3">
          {(a?.invariant.currencies ?? [null, null]).map((c, i) => (
            <div key={c?.currency ?? i} className="well p-4">
              <div className="flex items-center justify-between">
                {c ? <p className="eyebrow">{c.currency}</p> : <Skeleton className="h-3 w-10" />}
                {c ? (
                  c.holds ? (
                    <Chip tone="ok" icon={<IconCheck size={12} />}>Holds</Chip>
                  ) : (
                    <Chip tone="bad" icon={<IconX size={12} />}>Broken</Chip>
                  )
                ) : (
                  <Skeleton className="h-7 w-20 !rounded-full" />
                )}
              </div>
              <dl className="tnum mt-3 space-y-1.5 text-[13px]">
                {[
                  ['Supply at init', c ? money(c.supply, c.currency) : undefined],
                  ['Sum of balances', c ? money(c.sum, c.currency) : undefined],
                  ['Accounts summed', c ? String(c.accounts) : undefined],
                ].map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-3">
                    <dt className="text-muted">{k}</dt>
                    <dd className="font-medium">{v ?? <Skeleton className="h-4 w-28" />}</dd>
                  </div>
                ))}
              </dl>
            </div>
          ))}
        </div>
        <p className="mt-5 text-xs leading-relaxed text-muted">Value-conservation invariant enforced in chaincode after every settlement.</p>
      </section>
    </div>
  )
}

/**
 * Entries are numbered 1..head with no gaps (appendLog in ledger.go), so the
 * highest n is the true count. If fewer came back, say so instead of
 * presenting a partial log as complete.
 */
export function Completeness({ returned, maxN, what = 'audit log entries' }: { returned: number; maxN: number; what?: string }) {
  if (returned >= maxN) return null
  return (
    <div role="status" className="mb-4 flex items-start gap-3 rounded-xl bg-warn-soft p-4 text-warn ring-1 ring-inset ring-warn-line">
      <IconAlert size={18} className="mt-0.5 shrink-0" />
      <div className="text-sm">
        <p className="font-semibold">
          Incomplete: the ledger query returned <span className="tnum">{returned}</span> of <span className="tnum">{maxN}</span> {what}.
        </p>
        <p className="mt-1 text-fg-2">
          Entries are numbered without gaps, so {maxN - returned} are missing from this response. Chaincode 1.1 reads the log by
          sequence number; seeing this means an older chaincode is deployed.
        </p>
      </div>
    </div>
  )
}

function Fact({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="well min-w-[112px] px-4 py-3">
      <dt className="eyebrow">{label}</dt>
      <dd className="tnum mt-1.5 text-[24px] font-semibold leading-none tracking-tight">
        {value ?? <Skeleton className="mt-0.5 h-6 w-12" />}
      </dd>
    </div>
  )
}

function RowsSkeleton({ rows }: { rows: number }) {
  return (
    <div className="divide-y divide-line px-7" aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-6 py-3.5">
          <Skeleton className="h-3.5 w-8" />
          <Skeleton className="h-5 w-24" />
          <Skeleton className="h-3.5 w-32" />
          <Skeleton className="h-3.5 w-20" />
          <Skeleton className="h-3.5 flex-1" />
          <Skeleton className="h-3.5 w-28" />
        </div>
      ))}
    </div>
  )
}

function TypeTag({ type }: { type: string }) {
  const tone =
    type === 'SETTLED' || type === 'NET_SETTLED' || type === 'LIQUIDITY_SETTLED'
      ? 'bg-ok-soft text-ok ring-ok-line'
      : type === 'INSTRUCTED'
        ? 'bg-surface-2 text-fg-2 ring-line-strong'
        : type === 'RATE_PUBLISHED'
          ? 'bg-accent-soft text-accent-fg ring-accent/25'
          : 'bg-warn-soft text-warn ring-warn-line'
  return (
    <span className={cx('inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 font-mono text-[11px] font-semibold ring-1 ring-inset', tone)}>
      <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
      {type}
    </span>
  )
}
