import { useEffect, useMemo, useState } from 'react'
import { getAudit, getState, netSettle, previewNet } from '../api/client'
import type { Balances, NetLeg, NetPreview, NetSettleResponse, Party, Trade } from '../api/types'
import { IconArrows, IconCheck, IconLock, IconRefresh, IconX } from '../components/Icons'
import { Money } from '../components/Money'
import { Meta, RejectedPanel, SettledPanel, type Reread } from '../components/Outcome'
import { TestTradeToggle } from '../components/TestTradeToggle'
import { Button, Card, Chip, Empty, ErrorBox, Skeleton } from '../components/ui'
import { cx } from '../lib/cx'
import { bankLabel, LEDGER_BANKS, money, shortHash, timeOf } from '../lib/format'
import { netTotal, reductionPct } from '../lib/net'
import { plainReason } from '../lib/reasons'
import { demoFirst, isTestTrade } from '../lib/trades'
import { useApi } from '../lib/useApi'

const CCYS = ['INR', 'USD'] as const

type Outcome =
  | { kind: 'idle' }
  | { kind: 'settling' }
  | { kind: 'settled'; res: NetSettleResponse }
  | { kind: 'rejected'; res: NetSettleResponse; reread: Reread }
  | { kind: 'error'; message: string }

function sameBalances(a?: Balances, b?: Balances) {
  if (!a || !b) return false
  return LEDGER_BANKS.every((bk) => CCYS.every((c) => (a[bk]?.[c] ?? 0) === (b[bk]?.[c] ?? 0)))
}

export function Netting({ online }: { online: boolean }) {
  const state = useApi(getState, 10_000)
  const audit = useApi(getAudit, 15_000)
  const [picked, setPicked] = useState<string[]>([])
  const [as, setAs] = useState<Party>('BANKIN')
  const [preview, setPreview] = useState<{ ids: string; data?: NetPreview; error?: string; loading: boolean } | null>(null)
  const [outcome, setOutcome] = useState<Outcome>({ kind: 'idle' })

  const [hideTests, setHideTests] = useState(true)
  const allMatched = useMemo(() => demoFirst((state.data?.trades ?? []).filter((t) => t.status === 'MATCHED')), [state.data])
  const matched = useMemo(() => (hideTests ? allMatched.filter((t) => !isTestTrade(t.tradeId)) : allMatched), [allMatched, hideTests])
  const byId = useMemo(() => new Map((state.data?.trades ?? []).map((t) => [t.tradeId, t])), [state.data])
  const selected = picked.filter((id) => byId.get(id)?.status === 'MATCHED' || outcome.kind !== 'idle')
  const key = [...selected].sort().join(',')

  // Ask the chaincode for gross vs net whenever the selection changes.
  useEffect(() => {
    if (selected.length < 2 || !online) return
    let live = true
    const ids = key.split(',')
    const t = window.setTimeout(async () => {
      setPreview({ ids: key, loading: true })
      const r = await previewNet(ids)
      if (!live) return
      setPreview(r.ok ? { ids: key, data: r.data, loading: false } : { ids: key, error: r.error, loading: false })
    }, 250)
    return () => {
      live = false
      window.clearTimeout(t)
    }
  }, [key, online, selected.length])

  const pv = preview && preview.ids === key && selected.length >= 2 ? preview : null
  const plan = pv?.data?.plan

  function toggle(id: string) {
    setOutcome({ kind: 'idle' })
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))
  }
  function selectAll() {
    setOutcome({ kind: 'idle' })
    setPicked(matched.length === picked.length ? [] : matched.map((t) => t.tradeId))
  }

  async function onSettle() {
    const ids = key.split(',')
    setOutcome({ kind: 'settling' })
    const r = await netSettle(ids, as)
    if (!r.ok) return setOutcome({ kind: 'error', message: r.error })
    if (r.data.result.outcome.ok) {
      setOutcome({ kind: 'settled', res: r.data })
      void state.reload()
      void audit.reload()
      return
    }
    setOutcome({ kind: 'rejected', res: r.data, reread: 'pending' })
    const fresh = await state.reload()
    const reread: Reread = !fresh.ok ? 'failed' : sameBalances(r.data.result.before?.balances, fresh.data.snapshot.balances) ? 'unchanged' : 'changed'
    setOutcome((o) => (o.kind === 'rejected' ? { ...o, reread } : o))
  }

  const settled = outcome.kind === 'settled' ? outcome.res : null
  const shownPlan = settled?.batch ?? plan
  const canSettle = online && selected.length >= 2 && !!plan && outcome.kind !== 'settling' && outcome.kind !== 'settled'
  const batches = (audit.data?.log ?? []).filter((e) => e.type === 'NET_SETTLED').sort((a, b) => b.n - a.n)

  return (
    <div className="grid grid-cols-12 gap-6">
      {online && state.error && !state.loading && (
        <div className="col-span-12">
          <ErrorBox title="Could not read ledger state (GET /api/state)" message={state.error} onRetry={() => void state.reload()} />
        </div>
      )}

      <section aria-labelledby="net-title" className="card card-raised hero col-span-12 p-8 lg:p-10">
        <span className="hero-edge" aria-hidden="true" />
        <div className="max-w-2xl">
          <p className="eyebrow !text-accent-fg">Bilateral netting</p>
          <h2 id="net-title" className="mt-2 text-balance text-[30px] font-semibold leading-tight tracking-[-0.025em]">
            Settle one net amount instead of every trade
          </h2>
          <p className="mt-3 text-[15px] leading-relaxed text-muted">
            Instead of settling every trade one by one, both banks settle a single net amount. Same both-or-neither
            guarantee, far less money moved.
          </p>
        </div>

        <div className="mt-10 grid grid-cols-1 items-center gap-6 lg:grid-cols-[1fr_200px_1fr]">
          <GrossPanel plan={shownPlan} count={settled ? settled.batch?.tradeIds.length ?? selected.length : selected.length} loading={!!pv?.loading} />
          <div className="flex flex-col items-center gap-4 py-2">
            <div className="flex w-full items-center">
              <span className="rail hidden lg:block" data-state={settled ? 'ok' : outcome.kind === 'rejected' ? 'bad' : shownPlan ? 'idle' : 'offline'} aria-hidden="true" />
              <div className="orb mx-3 shrink-0" data-state={settled ? 'ok' : outcome.kind === 'rejected' ? 'bad' : outcome.kind === 'settling' ? 'busy' : shownPlan ? 'idle' : 'offline'}>
                {settled ? <IconCheck size={30} /> : outcome.kind === 'rejected' ? <IconX size={30} /> : <IconArrows size={28} />}
              </div>
              <span className="rail rail-r hidden lg:block" data-state={settled ? 'ok' : outcome.kind === 'rejected' ? 'bad' : shownPlan ? 'idle' : 'offline'} aria-hidden="true" />
            </div>
            <div className="text-center">
              <p className={cx('text-sm font-semibold', settled ? 'text-ok' : outcome.kind === 'rejected' ? 'text-bad' : 'text-fg')}>
                {settled ? 'Settled together' : outcome.kind === 'rejected' ? 'Nothing moved' : 'Netted in one transaction'}
              </p>
              <p className="mt-0.5 text-xs text-muted">All trades or none</p>
            </div>
          </div>
          <NetPanel plan={shownPlan} loading={!!pv?.loading} />
        </div>

        {shownPlan && <Reduction plan={shownPlan} />}

        <div className="mt-8 flex flex-wrap items-center justify-between gap-6 border-t border-line pt-6">
          <div className="min-w-0 text-sm">
            {selected.length < 2 ? (
              <p className="text-muted">Select at least two matched trades below to see the net.</p>
            ) : pv?.error ? (
              <p className="text-bad">
                <span className="font-semibold">This batch cannot settle: </span>
                {plainReason(/ERR_[A-Z_]+/.exec(pv.error)?.[0])}
              </p>
            ) : pv?.data ? (
              pv.data.fundsOk ? (
                <p className="inline-flex items-center gap-2 font-medium text-ok">
                  <IconCheck size={16} /> Both banks can fund the net amounts.
                </p>
              ) : (
                <p className="font-medium text-bad">The net exceeds what a bank holds, so this batch would be refused. Nothing would move.</p>
              )
            ) : (
              <Skeleton className="h-5 w-72" />
            )}
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <fieldset className="flex h-12 items-center gap-1 rounded-xl bg-surface-3/70 p-1 ring-1 ring-inset ring-line">
              <legend className="sr-only">Submit as</legend>
              {(['BANKIN', 'BANKFX'] as const).map((p) => (
                <label
                  key={p}
                  className={cx(
                    'flex h-10 cursor-pointer items-center rounded-lg px-3.5 text-sm font-semibold transition-all has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--focus)]',
                    as === p ? 'card !rounded-lg text-fg' : 'text-muted hover:text-fg',
                  )}
                >
                  <input type="radio" name="net-as" className="sr-only" checked={as === p} onChange={() => setAs(p)} />
                  As {bankLabel(p)}
                </label>
              ))}
            </fieldset>
            <Button size="lg" onClick={() => void onSettle()} disabled={!canSettle} loading={outcome.kind === 'settling'}>
              <IconLock size={18} />
              {outcome.kind === 'settling' ? 'Settling…' : `Settle net${selected.length >= 2 ? ` (${selected.length} trades)` : ''}`}
            </Button>
          </div>
        </div>
        {pv?.error && (
          <details className="mt-3 text-sm">
            <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Technical details</summary>
            <p className="mt-2 break-words font-mono text-xs text-fg-2">{pv.error}</p>
          </details>
        )}
        {pv?.data && !pv.data.fundsOk && (
          <details className="mt-3 text-sm">
            <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Technical details</summary>
            <p className="mt-2 break-words font-mono text-xs text-fg-2">{pv.data.fundsMessage}</p>
          </details>
        )}

        <div aria-live="polite">
          {outcome.kind === 'settled' && (
            <SettledPanel result={outcome.res.result} headline={`Settled: ${outcome.res.batch?.tradeIds.length ?? selected.length} trades as one net movement, all together`}>
              <p className="mt-2 text-[15px] text-fg">
                Every trade in the batch is now settled, and only the net amounts moved between the banks.
              </p>
              {outcome.res.batch && (
                <dl className="mt-4 grid gap-x-8 gap-y-3 sm:grid-cols-3">
                  <Meta k="Batch" v={<span className="font-mono">{outcome.res.batch.batchId}</span>} />
                  <Meta k="Trades settled" v={<span className="tnum">{outcome.res.batch.tradeIds.length}</span>} />
                  <Meta k="Submitted by" v={outcome.res.batch.submittedBy} />
                </dl>
              )}
            </SettledPanel>
          )}
          {outcome.kind === 'rejected' && (
            <RejectedPanel result={outcome.res.result} reread={outcome.reread} headline="Rejected: nothing moved, balances unchanged" />
          )}
          {outcome.kind === 'error' && (
            <div className="mt-6">
              <ErrorBox title="The gateway did not return a netting outcome" message={outcome.message} />
            </div>
          )}
        </div>
      </section>

      <Card
        className="col-span-12 xl:col-span-7"
        title="Matched trades"
        subtitle="Mix directions to create a saving"
        labelledBy="net-pick"
        action={
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="secondary" size="sm" onClick={selectAll} disabled={matched.length === 0}>
              {matched.length > 0 && matched.length === picked.length ? 'Clear' : 'Select all'}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => void state.reload()} loading={state.refreshing && !!state.data} ariaLabel="Re-read trades">
              <IconRefresh size={14} />
            </Button>
          </div>
        }
      >
        <div className="-mt-2 mb-3 flex justify-end">
          <TestTradeToggle hide={hideTests} onChange={setHideTests} hidden={allMatched.length - matched.length} />
        </div>
        {!state.data ? (
          <div className="space-y-3" aria-hidden="true">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : matched.length === 0 ? (
          <Empty>No matched trades. Create some on the Settlement screen: both banks instruct the same terms.</Empty>
        ) : (
          <ul className="scroll-thin max-h-[26rem] divide-y divide-line overflow-auto">
            {matched.map((t) => (
              <TradeRow key={t.tradeId} t={t} checked={picked.includes(t.tradeId)} onToggle={() => toggle(t.tradeId)} />
            ))}
          </ul>
        )}
      </Card>

      <Card className="col-span-12 xl:col-span-5" title="Net batches settled" subtitle="From the append-only audit log" labelledBy="net-batches">
        {!audit.data ? (
          <div className="space-y-3" aria-hidden="true">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : batches.length === 0 ? (
          <Empty>No net batches yet.</Empty>
        ) : (
          <ol className="scroll-thin max-h-[26rem] divide-y divide-line overflow-auto">
            {batches.map((e) => (
              <li key={e.n} className="flex items-start gap-4 py-3.5">
                <span className="mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-full bg-ok-soft text-ok ring-1 ring-inset ring-ok-line">
                  <IconCheck size={14} />
                </span>
                <div className="min-w-0">
                  <p className="font-mono text-[13px] font-semibold">{e.ref}</p>
                  <p className="mt-0.5 text-xs text-muted">
                    {timeOf(e.txTimestamp)} · {e.submitterMsp} · tx <span className="font-mono">{shortHash(e.txId, 10)}</span>
                  </p>
                  <p className="mt-1 text-xs leading-relaxed text-fg-2">{e.detail}</p>
                </div>
              </li>
            ))}
          </ol>
        )}
      </Card>
    </div>
  )
}

function TradeRow({ t, checked, onToggle }: { t: Trade; checked: boolean; onToggle: () => void }) {
  return (
    <li>
      <label className={cx('flex cursor-pointer items-center gap-4 rounded-xl px-2 py-3 transition-colors hover:bg-surface-2', checked && 'bg-accent-soft hover:bg-accent-soft')}>
        <input type="checkbox" checked={checked} onChange={onToggle} className="h-4 w-4 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0 flex-1">
          <span className="block font-mono text-[13px] font-semibold">{t.tradeId}</span>
          <span className="mt-1 flex items-center gap-2 text-xs text-muted">
            <Chip tone={t.usdDeliverer === 'BANKFX' ? 'neutral' : 'accent'} className="!h-5 !px-2 text-[10px]">
              {bankLabel(t.usdDeliverer)} pays USD
            </Chip>
            {bankLabel(t.inrDeliverer)} pays INR
          </span>
        </span>
        <span className="tnum text-right text-[13px]">
          <span className="block font-semibold">{money(t.usdAmount, 'USD')}</span>
          <span className="block text-muted">{money(t.inrAmount, 'INR')}</span>
        </span>
      </label>
    </li>
  )
}

function GrossPanel({ plan, count, loading }: { plan?: { gross: Record<string, number> }; count: number; loading: boolean }) {
  return (
    <div className="card p-7">
      <div className="flex items-center justify-between">
        <p className="eyebrow">Gross: every trade on its own</p>
        <Chip>{count} {count === 1 ? 'trade' : 'trades'}</Chip>
      </div>
      <div className="mt-6 space-y-4">
        {CCYS.map((c) => (
          <div key={c} className="min-h-[40px]">
            {plan && !loading ? <Money minor={plan.gross[c] ?? 0} ccy={c} size="lg" className="text-muted" /> : <Skeleton className="h-9 w-[70%]" />}
          </div>
        ))}
      </div>
      <p className="mt-6 border-t border-line pt-4 text-xs text-muted">Total that would move if each trade settled separately, both directions.</p>
    </div>
  )
}

function NetPanel({ plan, loading }: { plan?: { net: NetLeg[] }; loading: boolean }) {
  return (
    <div className="card p-7">
      <p className="eyebrow !text-accent-fg">Net: what actually moves</p>
      <div className="mt-6 space-y-4">
        {CCYS.map((c) => {
          const legs = plan?.net.filter((x) => x.currency === c && x.amount > 0) ?? []
          const total = plan ? netTotal(plan.net, c) : 0
          return (
            <div key={c} className="min-h-[40px]">
              {plan && !loading ? (
                <div>
                  <Money minor={total} ccy={c} size="lg" />
                  <p className="mt-1 text-xs text-muted">
                    {legs.length === 0 ? 'Nets to zero: nothing moves' : legs.map((n) => `${bankLabel(n.from!)} pays ${bankLabel(n.to!)} ${money(n.amount, c)}`).join(' · ')}
                  </p>
                </div>
              ) : (
                <Skeleton className="h-9 w-[70%]" />
              )}
            </div>
          )
        })}
      </div>
      <p className="mt-6 border-t border-line pt-4 text-xs text-muted">The net payments, in one transaction with every trade.</p>
    </div>
  )
}

function Reduction({ plan }: { plan: { gross: Record<string, number>; net: NetLeg[] } }) {
  const saved = CCYS.some((c) => (plan.gross[c] ?? 0) > netTotal(plan.net, c))
  if (!saved) {
    return (
      <p className="well mt-6 px-5 py-4 text-sm text-fg-2">
        <span className="font-semibold text-fg">No reduction for this batch: </span>
        every selected trade runs the same way, so the net equals the gross. Netting saves money when trades run in both
        directions: add a trade where BankIN pays USD.
      </p>
    )
  }
  return (
    <div className="mt-6 grid gap-4 sm:grid-cols-2">
      {CCYS.map((c) => {
        const gross = plan.gross[c] ?? 0
        const net = netTotal(plan.net, c)
        const pct = reductionPct(gross, net)
        return (
          <div key={c} className="well flex items-baseline justify-between gap-4 px-5 py-4">
            <div>
              <p className="eyebrow">{c} not moved thanks to netting</p>
              <Money minor={gross - net} ccy={c} size="md" className="mt-1" />
            </div>
            <p className="tnum text-[40px] font-semibold leading-none tracking-[-0.03em] text-accent-fg">
              {pct}
              <span className="text-xl">%</span>
            </p>
          </div>
        )
      })}
    </div>
  )
}
