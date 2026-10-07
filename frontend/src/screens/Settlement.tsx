import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { getAudit, getQuote, getState, getTrade, publishRate, settleTrade, submitInstruction } from '../api/client'
import type { Audit, Balances, Party, Quote, RateRecord, Trade, WriteResult } from '../api/types'
import { IconCheck, IconClock, IconLink, IconLock, IconPlus, IconRefresh, IconX } from '../components/Icons'
import { Money } from '../components/Money'
import { Meta, RejectedPanel, SettledPanel } from '../components/Outcome'
import { TestTradeToggle } from '../components/TestTradeToggle'
import { Button, Card, Chip, Empty, ErrorBox, OutcomeLine, Skeleton, StatusChip } from '../components/ui'
import { cx, inputClass } from '../lib/cx'
import {
  bankLabel,
  LEDGER_BANKS,
  money,
  parseDecimalToMinor,
  rateFromMicros,
  shortHash,
  SIMULATED_BANK_NOTE,
  SIMULATED_BANKS,
  timeOf,
} from '../lib/format'
import { demoFirst, isTestTrade } from '../lib/trades'
import { useApi, type ApiState } from '../lib/useApi'
import { Completeness } from './Audit'

const BANKS = ['BANKIN', 'BANKFX'] as const
const CCYS = ['INR', 'USD'] as const

/** What the hero shows. Every non-idle phase is set from a real API response. */
type Phase =
  | { kind: 'idle' }
  | { kind: 'settling'; tradeId: string }
  | { kind: 'settled'; tradeId: string; result: WriteResult }
  | { kind: 'rejected'; tradeId: string; result: WriteResult; reread: 'pending' | 'unchanged' | 'changed' | 'failed' }
  | { kind: 'error'; tradeId: string; message: string }

function sameBalances(a: Balances | undefined, b: Balances | undefined): boolean {
  if (!a || !b) return false
  return LEDGER_BANKS.every((bk) => CCYS.every((c) => (a[bk]?.[c] ?? 0) === (b[bk]?.[c] ?? 0)))
}

function newTradeId(): string {
  // Only an identifier the user can edit; it carries no data.
  return 'USDINR-' + Date.now().toString(36).toUpperCase()
}

export function Settlement({ online }: { online: boolean }) {
  const state = useApi(getState, 10_000)
  const audit = useApi(getAudit, 15_000)
  const [selected, setSelected] = useState<string | null>(null)
  const [as, setAs] = useState<Party>('BANKIN')
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' })
  // Trades created or opened here are also read one by one with GET
  // /api/trades/{id}, so they show up at once instead of on the next list poll.
  const [tracked, setTracked] = useState<string[]>([])
  const [direct, setDirect] = useState<Record<string, Trade>>({})

  async function readTracked(ids: string[]) {
    const res = await Promise.all(ids.map((id) => getTrade(id)))
    setDirect((prev) => {
      const next = { ...prev }
      ids.forEach((id, i) => {
        const r = res[i]
        if (r.ok) next[id] = r.data.trade
        else delete next[id]
      })
      return next
    })
  }
  const track = (id: string) => setTracked((t) => (t.includes(id) ? t : [...t, id]))

  // Re-read tracked trades whenever the ledger state is re-read.
  useEffect(() => {
    if (tracked.length) void readTracked(tracked)
  }, [tracked, state.data])

  const trades = useMemo(() => {
    const m = new Map((state.data?.trades ?? []).map((t) => [t.tradeId, t]))
    for (const t of Object.values(direct)) m.set(t.tradeId, t)
    return demoFirst([...m.values()])
  }, [state.data, direct])
  const matched = trades.filter((t) => t.status === 'MATCHED')
  const rates = state.data?.rates
  const current: RateRecord | undefined = rates?.rates.find((r) => r.seq === rates.head)

  const phaseTradeId = phase.kind === 'idle' ? null : phase.tradeId
  const selectedTrade: Trade | undefined = trades.find((t) => t.tradeId === (selected ?? phaseTradeId)) ?? matched[0]

  async function onSettle() {
    if (!selectedTrade) return
    const id = selectedTrade.tradeId
    setSelected(id)
    setPhase({ kind: 'settling', tradeId: id })
    const r = await settleTrade(id, as)
    if (!r.ok) {
      setPhase({ kind: 'error', tradeId: id, message: r.error })
      return
    }
    const result = r.data
    if (result.outcome.ok) {
      setPhase({ kind: 'settled', tradeId: id, result })
      void state.reload()
      void audit.reload()
      return
    }
    // Refused. Show red immediately, then prove nothing moved with a fresh read.
    setPhase({ kind: 'rejected', tradeId: id, result, reread: 'pending' })
    const fresh = await state.reload()
    const reread = !fresh.ok
      ? 'failed'
      : sameBalances(result.before?.balances, fresh.data.snapshot.balances)
        ? 'unchanged'
        : 'changed'
    setPhase((p) => (p.kind === 'rejected' && p.tradeId === id ? { ...p, reread } : p))
  }

  function selectTrade(id: string) {
    setSelected(id)
    setPhase({ kind: 'idle' })
  }

  /** Returns the gateway's error text, or null once the trade is loaded. */
  async function openById(id: string): Promise<string | null> {
    const r = await getTrade(id)
    if (!r.ok) return r.error
    setDirect((d) => ({ ...d, [id]: r.data.trade }))
    track(id)
    selectTrade(id)
    return null
  }

  // No data yet (loading, or the gateway is unreachable): the full layout
  // still renders, with skeletons where figures would be. Nothing is invented.
  const pending = !state.data
  const snap = state.data?.snapshot
  const settledResult = phase.kind === 'settled' ? phase.result : null
  const rejected = phase.kind === 'rejected' ? phase : null

  return (
    <div className="grid grid-cols-12 gap-6" aria-busy={state.loading || undefined}>
      {online && state.error && !state.loading && (
        <div className="col-span-12">
          <ErrorBox title="Could not read ledger state (GET /api/state)" message={state.error} onRetry={() => void state.reload()} />
        </div>
      )}

      <HeroSettle
        className="col-span-12"
        pending={pending}
        trade={selectedTrade}
        matched={matched}
        phase={phase}
        as={as}
        setAs={setAs}
        onSelect={selectTrade}
        onSettle={() => void onSettle()}
        onOpen={openById}
        online={online}
      />

      {BANKS.map((b) => (
        <BalanceCard
          // A new key per settle outcome remounts the card, replaying its animation once.
          key={`${b}-${phase.kind}-${phaseTradeId ?? ''}`}
          className="col-span-12 md:col-span-6 xl:col-span-4"
          bank={b}
          balances={snap?.balances}
          flash={settledResult ? 'ok' : rejected ? 'bad' : null}
          before={settledResult?.before?.balances ?? null}
          after={settledResult?.after?.balances ?? null}
          unchanged={rejected?.reread === 'unchanged'}
          readAt={snap?.readAt}
          refreshing={state.refreshing && !pending}
        />
      ))}

      <RateCard
        className="col-span-12 xl:col-span-4"
        pending={pending}
        current={current}
        head={rates?.head ?? 0}
        window={rates?.rateWindow ?? 0}
        online={online}
        onPublished={() => {
          void state.reload()
          void audit.reload()
        }}
      />

      {Object.keys(SIMULATED_BANKS).map((b) => (
        <BalanceCard
          key={`${b}-${phase.kind}-${phaseTradeId ?? ''}`}
          className="col-span-12 md:col-span-6"
          bank={b}
          balances={snap?.balances}
          flash={null}
          before={settledResult?.before?.balances ?? null}
          after={settledResult?.after?.balances ?? null}
          unchanged={rejected?.reread === 'unchanged'}
          readAt={snap?.readAt}
          refreshing={state.refreshing && !pending}
        />
      ))}

      <NewTrade
        className="col-span-12 xl:col-span-5"
        head={rates?.head ?? 0}
        balances={snap?.balances}
        online={online && !pending}
        onTracked={(id) => {
          track(id)
          void readTracked([id])
        }}
        onCreated={async (id) => {
          track(id)
          await readTracked([id])
          void state.reload()
          selectTrade(id)
        }}
      />

      <History className="col-span-12 xl:col-span-7" trades={trades} audit={audit} online={online} />

      <TradesTable
        className="col-span-12"
        pending={pending}
        trades={trades}
        selectedId={selectedTrade?.tradeId}
        onSelect={selectTrade}
        onRefresh={() => void state.reload()}
        refreshing={state.refreshing}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Hero: the two legs and the atomic link between them. Outcome colour changes
// only from `phase`, which only an API response can set.

type LegState = 'idle' | 'busy' | 'ok' | 'ok-static' | 'bad' | 'offline'

function HeroSettle({
  className,
  pending,
  trade,
  matched,
  phase,
  as,
  setAs,
  onSelect,
  onSettle,
  onOpen,
  online,
}: {
  className?: string
  pending: boolean
  trade: Trade | undefined
  matched: Trade[]
  phase: Phase
  as: Party
  setAs: (p: Party) => void
  onSelect: (id: string) => void
  onSettle: () => void
  onOpen: (id: string) => Promise<string | null>
  online: boolean
}) {
  const [openId, setOpenId] = useState('')
  const [openErr, setOpenErr] = useState<string | null>(null)
  const [opening, setOpening] = useState(false)

  const forThis = trade && phase.kind !== 'idle' && phase.tradeId === trade.tradeId ? phase : { kind: 'idle' as const }
  const legState: LegState = !trade
    ? 'offline'
    : forThis.kind === 'settled'
      ? 'ok'
      : forThis.kind === 'rejected'
        ? 'bad'
        : forThis.kind === 'settling'
          ? 'busy'
          : trade.status === 'SETTLED'
            ? 'ok-static'
            : 'idle'
  const animKey = forThis.kind === 'idle' ? 'idle' : forThis.kind
  const canSettle = online && !!trade && trade.status === 'MATCHED' && forThis.kind !== 'settling'

  return (
    <section aria-labelledby="hero-title" className={cx('card card-raised hero p-8 lg:p-10', className)}>
      <span className="hero-edge" aria-hidden="true" />
      <div className="flex flex-wrap items-start justify-between gap-8">
        <div className="max-w-xl">
          <p className="eyebrow !text-accent-fg">Payment versus payment (PvP)</p>
          <h2 id="hero-title" className="mt-2 text-balance text-[30px] font-semibold leading-tight tracking-[-0.025em]">
            Both legs settle together, or nothing moves.
          </h2>
          <p className="mt-3 text-[15px] leading-relaxed text-muted">
            BankIN owes INR and BankFX owes USD. Both payments travel in one ledger transaction that both banks must approve,
            so either both land or neither does. No bank can be left having paid without being paid.
          </p>
        </div>

        <div className="well w-full space-y-4 p-4 sm:w-[320px]">
          <label className="flex flex-col gap-2">
            <span className="eyebrow">Matched trade</span>
            <select
              className={cx(inputClass, 'w-full font-mono text-sm')}
              value={trade?.tradeId ?? ''}
              onChange={(e) => onSelect(e.target.value)}
              disabled={pending || (matched.length === 0 && !trade)}
            >
              {pending && <option value="">Waiting for ledger state</option>}
              {!pending && trade && !matched.some((t) => t.tradeId === trade.tradeId) && (
                <option value={trade.tradeId}>
                  {trade.tradeId} ({trade.status})
                </option>
              )}
              {!pending && matched.length === 0 && !trade && <option value="">No matched trades</option>}
              {matched.map((t) => (
                <option key={t.tradeId} value={t.tradeId}>
                  {t.tradeId}
                </option>
              ))}
            </select>
          </label>
          <form
            className="flex items-end gap-2"
            onSubmit={async (e) => {
              e.preventDefault()
              const id = openId.trim()
              if (!id) return
              setOpening(true)
              setOpenErr(await onOpen(id))
              setOpening(false)
            }}
          >
            <label className="flex flex-1 flex-col gap-2">
              <span className="eyebrow">Open by trade ID</span>
              <input className={cx(inputClass, 'w-full font-mono text-sm')} value={openId} onChange={(e) => setOpenId(e.target.value)} placeholder="USDINR-…" />
            </label>
            <Button type="submit" variant="secondary" loading={opening} disabled={!online || !openId.trim()}>
              Open
            </Button>
          </form>
          {openErr && (
            <p role="alert" className="break-words font-mono text-xs text-bad">
              {openErr}
            </p>
          )}
        </div>
      </div>

      <div className="mt-10 grid grid-cols-1 items-center gap-6 lg:grid-cols-[1fr_220px_1fr]">
        <Leg
          key={`inr-${animKey}-${trade?.tradeId ?? ''}`}
          ccy="INR"
          from={trade?.inrDeliverer}
          to={trade?.usdDeliverer}
          amount={trade?.inrAmount}
          state={legState}
          pending={pending}
        />
        <AtomicLink state={legState} />
        <Leg
          key={`usd-${animKey}-${trade?.tradeId ?? ''}`}
          ccy="USD"
          from={trade?.usdDeliverer}
          to={trade?.inrDeliverer}
          amount={trade?.usdAmount}
          state={legState}
          pending={pending}
        />
      </div>

      <div className="mt-8 flex flex-wrap items-center justify-between gap-6 border-t border-line pt-6">
        <div className="min-w-0 text-sm text-muted">
          {trade ? (
            <dl className="flex flex-wrap gap-x-8 gap-y-2">
              <Meta k="Trade" v={<span className="font-mono text-fg">{trade.tradeId}</span>} />
              <Meta k="Rate" v={<span className="tnum text-fg">{rateFromMicros(trade.rateMicros)} INR / USD</span>} />
              <Meta k="Rate reference" v={<span className="tnum text-fg">#{trade.rateSeq}</span>} />
              <Meta k="Instructed by" v={<span className="text-fg">{trade.instructedBy.map(bankLabel).join(' and ') || 'nobody yet'}</span>} />
            </dl>
          ) : pending ? (
            <div className="flex gap-8">
              <Skeleton className="h-9 w-32" />
              <Skeleton className="h-9 w-40" />
              <Skeleton className="h-9 w-24" />
            </div>
          ) : (
            <p>No trade is ready to settle. Create one below: both banks instruct the same terms, and it becomes MATCHED.</p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <fieldset className="flex h-12 items-center gap-1 rounded-xl bg-surface-3/70 p-1 ring-1 ring-inset ring-line">
            <legend className="sr-only">Submit settlement as</legend>
            {(['BANKIN', 'BANKFX'] as const).map((p) => (
              <label
                key={p}
                className={cx(
                  'flex h-10 cursor-pointer items-center rounded-lg px-3.5 text-sm font-semibold transition-all has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--focus)]',
                  as === p ? 'card !rounded-lg text-fg' : 'text-muted hover:text-fg',
                )}
              >
                <input type="radio" name="settle-as" className="sr-only" checked={as === p} onChange={() => setAs(p)} />
                As {bankLabel(p)}
              </label>
            ))}
          </fieldset>
          <Button size="lg" onClick={onSettle} disabled={!canSettle} loading={forThis.kind === 'settling'}>
            <IconLock size={18} />
            {forThis.kind === 'settling' ? 'Settling…' : 'Settle both legs'}
          </Button>
        </div>
      </div>
      {online && trade && trade.status !== 'MATCHED' && forThis.kind === 'idle' && (
        <p className="mt-3 text-right text-sm text-muted">
          This trade is {trade.status.replace('_', ' ').toLowerCase()}; only a MATCHED trade can settle.
        </p>
      )}

      <div aria-live="polite">
        {forThis.kind === 'settled' && <SettledPanel result={forThis.result} headline="Settled: both legs moved together" />}
        {forThis.kind === 'rejected' && (
          <RejectedPanel result={forThis.result} reread={forThis.reread} headline="Rejected: nothing moved, balances unchanged" />
        )}
        {forThis.kind === 'error' && (
          <div className="mt-6">
            <ErrorBox title="The gateway did not return a settlement outcome" message={forThis.message} />
          </div>
        )}
      </div>
    </section>
  )
}

function Leg({
  ccy,
  from,
  to,
  amount,
  state,
  pending,
}: {
  ccy: string
  from?: string
  to?: string
  amount?: number
  state: LegState
  pending: boolean
}) {
  const anim = state === 'ok' ? 'anim-lock' : state === 'bad' ? 'anim-shake' : ''
  const dataState = state === 'ok' || state === 'ok-static' ? 'ok' : state === 'bad' ? 'bad' : undefined
  return (
    <div className={cx('leg card p-7 transition-all duration-200', anim)} data-state={dataState}>
      <div className="flex items-center justify-between gap-3">
        <p className="eyebrow">{from ? `${bankLabel(from)} owes ${ccy}` : `${ccy} leg`}</p>
        <LegBadge state={state} pending={pending} />
      </div>
      <div className={cx('mt-6 min-h-[56px]', state === 'bad' && 'line-through decoration-bad decoration-2')}>
        {amount !== undefined ? <Money minor={amount} ccy={ccy} size="hero" /> : <Skeleton className="h-14 w-[78%] max-w-[340px]" />}
      </div>
      <div className="mt-7 grid grid-cols-2 gap-4 border-t border-line pt-5">
        <div>
          <p className="eyebrow">Payer</p>
          {from ? <p className="mt-1.5 text-[15px] font-semibold">{bankLabel(from)}</p> : <Skeleton className="mt-2 h-5 w-20" />}
        </div>
        <div>
          <p className="eyebrow">Receiver</p>
          {to ? <p className="mt-1.5 text-[15px] font-semibold">{bankLabel(to)}</p> : <Skeleton className="mt-2 h-5 w-20" />}
        </div>
      </div>
    </div>
  )
}

function LegBadge({ state, pending }: { state: LegState; pending: boolean }) {
  if (state === 'ok') return <Chip tone="ok" icon={<IconCheck size={13} />}>Paid</Chip>
  if (state === 'ok-static') return <Chip tone="ok" icon={<IconCheck size={13} />}>Settled</Chip>
  if (state === 'bad') return <Chip tone="bad" icon={<IconX size={13} />}>Not paid</Chip>
  if (state === 'busy') return <Chip tone="warn" icon={<IconClock size={13} />}>Approving</Chip>
  if (state === 'offline') return <Chip icon={<IconClock size={13} />}>{pending ? 'Loading' : 'No trade'}</Chip>
  return <Chip icon={<IconLock size={13} />}>Awaiting settlement</Chip>
}

function AtomicLink({ state }: { state: LegState }) {
  const s = state === 'ok' || state === 'ok-static' ? 'ok' : state === 'bad' ? 'bad' : state === 'busy' ? 'busy' : state === 'offline' ? 'offline' : 'idle'
  const label = s === 'ok' ? 'Moved together' : s === 'bad' ? 'Nothing moved' : s === 'busy' ? 'Both banks approving' : 'One transaction'
  const sub = s === 'ok' ? 'Both payments landed' : s === 'bad' ? 'Balances unchanged' : 'Needs both banks to approve'
  return (
    <div className="flex flex-col items-center gap-4 py-2">
      <div className="flex w-full items-center">
        <span className="rail hidden lg:block" data-state={s} aria-hidden="true" />
        <div className="orb mx-3 shrink-0" data-state={s}>
          {s === 'ok' ? <IconCheck size={30} /> : s === 'bad' ? <IconX size={30} /> : <IconLink size={28} />}
        </div>
        <span className="rail rail-r hidden lg:block" data-state={s} aria-hidden="true" />
      </div>
      <div className="text-center">
        <p className={cx('text-sm font-semibold', s === 'ok' ? 'text-ok' : s === 'bad' ? 'text-bad' : 'text-fg')}>{label}</p>
        <p className="mt-0.5 text-xs text-muted">{sub}</p>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------

const BANK_ROLE: Record<string, string> = {
  BANKIN: 'Holds tokenized INR',
  BANKFX: 'Holds tokenized USD',
  BANKUS: `${SIMULATED_BANK_NOTE}, custodied by ${SIMULATED_BANKS.BANKUS}'s org`,
  BANKSG: `${SIMULATED_BANK_NOTE}, custodied by ${SIMULATED_BANKS.BANKSG}'s org`,
}

function BalanceCard({
  className,
  bank,
  balances,
  flash,
  before,
  after,
  unchanged,
  readAt,
  refreshing,
}: {
  className?: string
  bank: string
  balances?: Balances
  flash: 'ok' | 'bad' | null
  before: Balances | null
  after: Balances | null
  unchanged: boolean
  readAt?: string
  refreshing: boolean
}) {
  return (
    <section
      aria-label={`${bankLabel(bank)} balances`}
      className={cx(
        'card flex flex-col p-6 transition-all lg:p-7',
        flash === 'ok' && 'anim-lock ring-4 ring-ok-soft',
        flash === 'bad' && 'ring-4 ring-bad-soft',
        className,
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-[17px] font-semibold tracking-tight">{bankLabel(bank)}</h2>
          <p className="mt-0.5 text-[13px] text-muted">{BANK_ROLE[bank]}</p>
        </div>
        {unchanged ? (
          <Chip tone="ok" icon={<IconCheck size={13} />}>Unchanged</Chip>
        ) : refreshing ? (
          <Chip icon={<IconRefresh size={13} />}>Reading</Chip>
        ) : null}
      </div>
      <div className="mb-6 mt-6 space-y-5">
        {CCYS.map((c) => {
          const d = before && after ? (after[bank]?.[c] ?? 0) - (before[bank]?.[c] ?? 0) : 0
          return (
            <div key={c}>
              <div className="flex items-center justify-between">
                <p className="eyebrow">{c} balance</p>
                {d !== 0 && (
                  <span className={cx('tnum text-[13px] font-semibold', d > 0 ? 'text-ok' : 'text-bad')}>
                    {d > 0 ? '+' : ''}
                    {money(d, c)}
                  </span>
                )}
              </div>
              <div className="mt-2 min-h-[32px]">
                {balances ? <Money minor={balances[bank]?.[c] ?? 0} ccy={c} size="lg" /> : <Skeleton className="h-8 w-[70%]" />}
              </div>
            </div>
          )
        })}
      </div>
      <p className="mt-auto border-t border-line pt-4 text-xs text-muted">
        {readAt ? <>Simulated tokenized cash · read {timeOf(readAt)}</> : 'Simulated tokenized cash · awaiting ledger read'}
      </p>
    </section>
  )
}

// ---------------------------------------------------------------------------

type Step = { label: string; status: 'pending' | 'running' | 'ok' | 'bad'; detail?: ReactNode }

function NewTrade({
  className,
  head,
  balances,
  online,
  onTracked,
  onCreated,
}: {
  className?: string
  head: number
  balances?: Balances
  online: boolean
  onTracked: (id: string) => void
  onCreated: (id: string) => void | Promise<void>
}) {
  const [tradeId, setTradeId] = useState(newTradeId)
  const [usd, setUsd] = useState('10000')
  const [payer, setPayer] = useState<'BANKFX' | 'BANKIN'>('BANKFX')
  const [steps, setSteps] = useState<Step[]>([])
  const [busy, setBusy] = useState(false)
  const cents = parseDecimalToMinor(usd, 2)
  const idOk = /^[A-Za-z0-9._-]{1,64}$/.test(tradeId)
  const payerUsd = balances?.[payer]?.USD

  // Steps already committed for this exact trade. A retry resumes after them
  // instead of re-instructing (which the chaincode would refuse as a duplicate).
  const [progress, setProgress] = useState<{ key: string; quote: Quote; done: [boolean, boolean] } | null>(null)
  const key = `${tradeId}|${cents}|${payer}`
  const resumable = !!progress && progress.key === key && !(progress.done[0] && progress.done[1])

  async function run() {
    if (!cents || !idOk) return
    setBusy(true)
    const prev = resumable ? progress : null
    const s: Step[] = [
      { label: `Quote the INR leg at oracle rate seq ${prev?.quote.rateSeq ?? head}`, status: prev ? 'ok' : 'running' },
      { label: 'BankIN instructs the terms', status: prev?.done[0] ? 'ok' : 'pending' },
      { label: 'BankFX instructs the same terms', status: prev?.done[1] ? 'ok' : 'pending' },
    ]
    if (prev) {
      s[0].detail = <>INR leg = {money(prev.quote.inrAmount, 'INR')}</>
      for (const st of s.slice(1)) if (st.status === 'ok') st.detail = <span>Committed in the earlier attempt</span>
    }
    setSteps([...s])
    let quote = prev?.quote
    if (!quote) {
      const q = await getQuote(cents, head)
      if (!q.ok) {
        s[0] = { ...s[0], status: 'bad', detail: <span className="font-mono">{q.error}</span> }
        setSteps([...s])
        setBusy(false)
        return
      }
      quote = q.data
      s[0] = { ...s[0], status: 'ok', detail: <>INR leg = {money(quote.inrAmount, 'INR')}</> }
    }
    const done: [boolean, boolean] = prev ? [...prev.done] : [false, false]
    const base = { tradeId, usdDeliverer: payer, usdAmount: String(quote.usdAmount), inrAmount: String(quote.inrAmount), rateSeq: quote.rateSeq }
    for (const [i, p] of [[1, 'BANKIN'], [2, 'BANKFX']] as const) {
      if (done[i - 1]) continue
      s[i] = { ...s[i], status: 'running' }
      setSteps([...s])
      const r = await submitInstruction({ ...base, as: p })
      const ok = r.ok && r.data.outcome.ok
      s[i] = {
        ...s[i],
        status: ok ? 'ok' : 'bad',
        detail: r.ok ? (
          <OutcomeLine ok={r.data.outcome.ok} code={r.data.outcome.code} message={r.data.outcome.message} stage={r.data.outcome.stage} />
        ) : (
          <span className="font-mono">{r.error}</span>
        ),
      }
      setSteps([...s])
      if (!ok) {
        setProgress({ key, quote, done })
        setBusy(false)
        return
      }
      done[i - 1] = true
      // The trade now exists on the ledger (PENDING_MATCH after the first
      // instruction), so start reading it by ID.
      if (i === 1) onTracked(tradeId)
    }
    setProgress(null)
    setBusy(false)
    await onCreated(tradeId)
    setTradeId(newTradeId())
  }

  return (
    <Card className={className} title="New trade" subtitle="Both banks instruct identical terms" labelledBy="new-trade">
      <form
        className="grid gap-5 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault()
          void run()
        }}
      >
        <label className="flex flex-col gap-2">
          <span className="eyebrow">Trade ID</span>
          <input className={cx(inputClass, 'font-mono text-sm')} value={tradeId} onChange={(e) => setTradeId(e.target.value)} aria-invalid={!idOk} />
        </label>
        <label className="flex flex-col gap-2">
          <span className="eyebrow">USD amount</span>
          <input
            className={cx(inputClass, 'tnum text-sm')}
            value={usd}
            inputMode="decimal"
            onChange={(e) => setUsd(e.target.value)}
            aria-invalid={!cents}
            aria-describedby="usd-hint"
          />
        </label>
        <fieldset className="sm:col-span-2">
          <legend className="eyebrow mb-2">USD payer (the other bank pays INR)</legend>
          <div className="inline-flex h-10 items-center gap-1 rounded-xl bg-surface-3/70 p-1 ring-1 ring-inset ring-line">
            {(['BANKFX', 'BANKIN'] as const).map((b) => (
              <label
                key={b}
                className={cx(
                  'flex h-8 cursor-pointer items-center rounded-lg px-4 text-sm font-semibold transition-all has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--focus)]',
                  payer === b ? 'card !rounded-lg text-fg' : 'text-muted hover:text-fg',
                )}
              >
                <input type="radio" name="payer" className="sr-only" checked={payer === b} onChange={() => setPayer(b)} />
                {bankLabel(b)}
              </label>
            ))}
          </div>
        </fieldset>
        <p id="usd-hint" className="text-[13px] leading-relaxed text-muted sm:col-span-2">
          {payerUsd !== undefined ? (
            <>
              {bankLabel(payer)} holds <span className="tnum font-semibold text-fg">{money(payerUsd, 'USD')}</span>.{' '}
            </>
          ) : null}
          To see a rollback, enter more than the USD payer holds: both banks can still agree the trade, but settlement is refused.
        </p>
        <div className="sm:col-span-2">
          <Button type="submit" variant="secondary" disabled={!online || !cents || !idOk || head === 0} loading={busy}>
            <IconPlus size={16} />
            {busy ? 'Instructing…' : resumable ? 'Retry the remaining steps' : 'Quote and instruct both banks'}
          </Button>
        </div>
      </form>
      {steps.length > 0 && (
        <ol className="mt-6 border-t border-line pt-5" aria-live="polite">
          {steps.map((s, i) => (
            <li key={i} className="relative flex gap-3 pb-4 last:pb-0">
              {i < steps.length - 1 && <span className="absolute left-[11px] top-6 h-[calc(100%-1rem)] w-px bg-line-strong" aria-hidden="true" />}
              <span
                className={cx(
                  'relative grid h-6 w-6 shrink-0 place-items-center rounded-full text-[11px] font-bold ring-1 ring-inset',
                  s.status === 'ok'
                    ? 'bg-ok-soft text-ok ring-ok-line'
                    : s.status === 'bad'
                      ? 'bg-bad-soft text-bad ring-bad-line'
                      : s.status === 'running'
                        ? 'bg-warn-soft text-warn ring-warn-line'
                        : 'bg-surface-2 text-muted ring-line-strong',
                )}
              >
                {s.status === 'ok' ? <IconCheck size={12} /> : s.status === 'bad' ? <IconX size={12} /> : i + 1}
              </span>
              <div className="min-w-0 text-sm">
                <p className="font-medium">
                  {s.label}
                  <span className="sr-only"> ({s.status})</span>
                </p>
                {s.detail && <div className="mt-0.5 break-words text-muted">{s.detail}</div>}
              </div>
            </li>
          ))}
        </ol>
      )}
    </Card>
  )
}

// ---------------------------------------------------------------------------

function RateCard({
  className,
  pending,
  current,
  head,
  window,
  online,
  onPublished,
}: {
  className?: string
  pending: boolean
  current: RateRecord | undefined
  head: number
  window: number
  online: boolean
  onPublished: () => void
}) {
  const [rate, setRate] = useState('83.250000')
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<{ ok: boolean; code?: string; message?: string; stage?: string; seq?: number } | { httpError: string } | null>(null)
  const micros = parseDecimalToMinor(rate, 6)

  async function publish() {
    if (!micros) return
    setBusy(true)
    const r = await publishRate(Number(micros))
    setBusy(false)
    if (!r.ok) return setRes({ httpError: r.error })
    const o = r.data.result.outcome
    setRes({ ok: o.ok, code: o.code, message: o.message, stage: o.stage, seq: r.data.attestation.seq })
    onPublished()
  }

  return (
    <section aria-labelledby="rate-card" className={cx('card flex flex-col p-6 lg:p-7', className)}>
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 id="rate-card" className="text-[17px] font-semibold tracking-tight">
            FX rate
          </h2>
          <p className="mt-0.5 text-[13px] text-muted">Signed oracle attestation</p>
        </div>
        {current && <Chip tone="neutral">Seq {current.seq}</Chip>}
      </div>

      <div className="mt-6">
        <p className="eyebrow">{current?.pair ?? 'USD / INR'}</p>
        <div className="mt-2 min-h-[32px]">
          {current ? (
            <span className="tnum inline-flex items-baseline gap-2">
              <span className="text-[32px] font-semibold leading-none tracking-[-0.02em] text-accent-fg">{rateFromMicros(current.rateMicros)}</span>
              <span className="text-xs font-semibold uppercase tracking-[0.14em] text-muted">INR per USD</span>
            </span>
          ) : pending ? (
            <Skeleton className="h-8 w-[60%]" />
          ) : (
            <p className="text-sm text-muted">No rate published yet.</p>
          )}
        </div>
      </div>

      <dl className="mt-6 grid grid-cols-2 gap-x-4 gap-y-4 border-t border-line pt-5 text-sm">
        {[
          ['Source', current?.source],
          ['As of', current ? timeOf(current.asOf) : undefined],
          ['Verified key', current ? shortHash(current.verifiedWith, 12) : undefined],
          ['Window', current ? `Latest ${window} of ${head}` : undefined],
        ].map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="eyebrow">{k}</dt>
            <dd className="mt-1 truncate font-medium">{v ?? (pending ? <Skeleton className="mt-1 h-4 w-24" /> : '–')}</dd>
          </div>
        ))}
      </dl>

      <form
        className="mt-auto flex items-end gap-2 border-t border-line pt-5"
        onSubmit={(e) => {
          e.preventDefault()
          void publish()
        }}
      >
        <label className="flex flex-1 flex-col gap-2">
          <span className="eyebrow">Publish a signed rate</span>
          <input className={cx(inputClass, 'tnum w-full text-sm')} value={rate} inputMode="decimal" onChange={(e) => setRate(e.target.value)} aria-invalid={!micros} />
        </label>
        <Button type="submit" variant="secondary" disabled={!online || !micros} loading={busy}>
          {busy ? 'Publishing…' : 'Sign and publish'}
        </Button>
      </form>
      {res && (
        <div className="mt-3" aria-live="polite">
          {'httpError' in res ? (
            <ErrorBox title="Publish failed" message={res.httpError} />
          ) : (
            <OutcomeLine ok={res.ok} code={res.code} message={res.ok ? `rate seq ${res.seq}` : res.message} stage={res.stage} />
          )}
        </div>
      )}
    </section>
  )
}

// ---------------------------------------------------------------------------

function TradesTable({
  className,
  pending,
  trades,
  selectedId,
  onSelect,
  onRefresh,
  refreshing,
}: {
  className?: string
  pending: boolean
  trades: Trade[]
  selectedId?: string
  onSelect: (id: string) => void
  onRefresh: () => void
  refreshing: boolean
}) {
  const [filter, setFilter] = useState<'ALL' | 'MATCHED' | 'PENDING_MATCH' | 'SETTLED'>('ALL')
  const [hideTests, setHideTests] = useState(true)
  const visible = useMemo(() => (hideTests ? trades.filter((t) => !isTestTrade(t.tradeId)) : trades), [trades, hideTests])
  const shown = filter === 'ALL' ? visible : visible.filter((t) => t.status === filter)
  const counts = useMemo(() => {
    const c: Record<string, number> = { ALL: visible.length }
    for (const t of visible) c[t.status] = (c[t.status] ?? 0) + 1
    return c
  }, [visible])
  return (
    <Card
      className={className}
      title="Trade ledger"
      subtitle="Every trade record on the channel"
      labelledBy="trades"
      action={
        <div className="flex flex-wrap items-center justify-end gap-4">
          <TestTradeToggle hide={hideTests} onChange={setHideTests} hidden={trades.length - visible.length} />
          <div className="inline-flex h-9 items-center gap-1 rounded-xl bg-surface-3/70 p-1 ring-1 ring-inset ring-line" role="group" aria-label="Filter trades by status">
            {(['ALL', 'MATCHED', 'PENDING_MATCH', 'SETTLED'] as const).map((f) => (
              <button
                key={f}
                onClick={() => setFilter(f)}
                aria-pressed={filter === f}
                className={cx(
                  'flex h-7 items-center gap-1.5 rounded-lg px-3 text-[13px] font-semibold transition-all',
                  filter === f ? 'card !rounded-lg text-fg' : 'text-muted hover:text-fg',
                )}
              >
                {f === 'ALL' ? 'All' : f === 'PENDING_MATCH' ? 'Pending' : f[0] + f.slice(1).toLowerCase()}
                <span className="tnum text-muted">{pending ? '–' : (counts[f] ?? 0)}</span>
              </button>
            ))}
          </div>
          <Button variant="secondary" size="sm" onClick={onRefresh} loading={refreshing} ariaLabel="Re-read trades">
            <IconRefresh size={14} />
          </Button>
        </div>
      }
    >
      {pending ? (
        <TableSkeleton rows={5} />
      ) : shown.length === 0 ? (
        <Empty>No trades{filter === 'ALL' ? ' yet' : ' with this status'}.</Empty>
      ) : (
        <div className="scroll-thin -mx-6 max-h-[28rem] overflow-auto lg:-mx-7">
          <table className="w-full min-w-[52rem] text-left text-sm">
            <thead className="sticky top-0 z-[1] bg-surface">
              <tr className="eyebrow border-b border-line">
                <th className="px-6 py-3 font-semibold lg:px-7">Trade</th>
                <th className="px-4 py-3 font-semibold">Status</th>
                <th className="px-4 py-3 text-right font-semibold">USD leg</th>
                <th className="px-4 py-3 text-right font-semibold">INR leg</th>
                <th className="px-4 py-3 text-right font-semibold">Rate seq</th>
                <th className="px-4 py-3 font-semibold">Instructed by</th>
                <th className="px-6 py-3 lg:px-7">
                  <span className="sr-only">Action</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {shown.map((t) => (
                <tr
                  key={t.tradeId}
                  className={cx('border-b border-line transition-colors last:border-0 hover:bg-surface-2', t.tradeId === selectedId && 'bg-accent-soft hover:bg-accent-soft')}
                >
                  <td className="px-6 py-3.5 font-mono text-[13px] lg:px-7">{t.tradeId}</td>
                  <td className="px-4 py-3.5">
                    <StatusChip status={t.status} />
                  </td>
                  <td className="tnum px-4 py-3.5 text-right">
                    <span className="font-medium">{money(t.usdAmount, 'USD')}</span>
                    <span className="block text-xs text-muted">{bankLabel(t.usdDeliverer)} pays</span>
                  </td>
                  <td className="tnum px-4 py-3.5 text-right">
                    <span className="font-medium">{money(t.inrAmount, 'INR')}</span>
                    <span className="block text-xs text-muted">{bankLabel(t.inrDeliverer)} pays</span>
                  </td>
                  <td className="tnum px-4 py-3.5 text-right text-muted">{t.rateSeq}</td>
                  <td className="px-4 py-3.5 text-muted">{t.instructedBy.map(bankLabel).join(', ')}</td>
                  <td className="px-6 py-3.5 text-right lg:px-7">
                    {t.status === 'MATCHED' && (
                      <Button variant="secondary" size="sm" onClick={() => onSelect(t.tradeId)} ariaLabel={`Select ${t.tradeId} to settle`}>
                        Select
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

function TableSkeleton({ rows }: { rows: number }) {
  return (
    <div className="space-y-0 divide-y divide-line" aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-6 py-4">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-7 w-24 !rounded-full" />
          <Skeleton className="ml-auto h-4 w-28" />
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-4 w-16" />
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------

function History({ className, trades, audit, online }: { className?: string; trades: Trade[]; audit: ApiState<Audit>; online: boolean }) {
  const [hideTests, setHideTests] = useState(true)
  const byId = useMemo(() => new Map(trades.map((t) => [t.tradeId, t])), [trades])
  const all = audit.data?.log ?? []
  const maxN = all.reduce((m, e) => Math.max(m, e.n), 0)
  // Newest first, by the log's own sequence number.
  const settledRows = all.filter((e) => e.type === 'SETTLED' || e.type === 'NET_SETTLED')
  // Net batches are never test-suite trades; gross rows are hidden by trade ID.
  const rows = (hideTests ? settledRows.filter((e) => e.type === 'NET_SETTLED' || !isTestTrade(e.ref)) : settledRows).sort((x, y) => y.n - x.n)
  return (
    <Card
      className={className}
      title="Settlement history"
      subtitle="Gross settlements and net batches, from the append-only audit log"
      labelledBy="history"
      action={<TestTradeToggle hide={hideTests} onChange={setHideTests} hidden={settledRows.length - rows.length} />}
    >
      {!audit.data ? (
        <>
          {online && audit.error && !audit.loading && (
            <div className="mb-4">
              <ErrorBox title="Could not read /api/audit" message={audit.error} onRetry={() => void audit.reload()} />
            </div>
          )}
          <TableSkeleton rows={4} />
        </>
      ) : (
        <>
          <Completeness returned={all.length} maxN={maxN} />
          {rows.length === 0 ? (
            <Empty>No settlements yet.</Empty>
          ) : (
            <ol className="scroll-thin max-h-[26rem] divide-y divide-line overflow-auto">
              {rows.map((e) => {
                const t = byId.get(e.ref)
                return (
                  <li key={e.n} className="flex items-center gap-4 py-3.5">
                    <span className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-ok-soft text-ok ring-1 ring-inset ring-ok-line">
                      <IconCheck size={14} />
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="flex items-center gap-2 text-sm font-semibold">
                        <span className="font-mono text-[13px]">{e.ref}</span>
                        {e.type === 'NET_SETTLED' && <Chip tone="accent" className="!h-5 !px-2 text-[10px]">Net batch</Chip>}
                        <span className="text-xs font-normal text-muted">#{e.n}</span>
                      </p>
                      <p className="mt-0.5 truncate text-xs text-muted">
                        {timeOf(e.txTimestamp)} · {e.submitterMsp} · tx <span className="font-mono">{shortHash(e.txId, 10)}</span>
                      </p>
                    </div>
                    {t && (
                      <div className="tnum text-right text-[13px]">
                        <p className="font-semibold">{money(t.usdAmount, 'USD')}</p>
                        <p className="text-muted">{money(t.inrAmount, 'INR')}</p>
                      </div>
                    )}
                  </li>
                )
              })}
            </ol>
          )}
        </>
      )}
    </Card>
  )
}
