import { useEffect, useMemo, useState } from 'react'
import { getAudit, getLiquidityScenarios, getState, liquiditySettle, previewLiquidity, resolveLiquidity } from '../api/client'
import type { Balances, LiquidityPlan, LiquidityPreview, LiquiditySettleResponse, Party, Trade } from '../api/types'
import { IconArrows, IconCheck, IconLock, IconRefresh } from '../components/Icons'
import { Money } from '../components/Money'
import { Meta, RejectedPanel, SettledPanel, type Reread } from '../components/Outcome'
import { Button, Card, Chip, Empty, ErrorBox, Skeleton } from '../components/ui'
import { cx } from '../lib/cx'
import { bankLabel, LEDGER_BANKS, money, shortHash, SIMULATED_BANK_NOTE, SIMULATED_BANKS, timeOf } from '../lib/format'
import { plainReason } from '../lib/reasons'
import { useApi } from '../lib/useApi'
import { netTotal, reductionPct } from '../lib/net'

const CCYS = ['INR', 'USD'] as const

type Outcome =
  | { kind: 'idle' }
  | { kind: 'settling' }
  | { kind: 'settled'; res: LiquiditySettleResponse }
  | { kind: 'rejected'; res: LiquiditySettleResponse; reread: Reread }
  | { kind: 'error'; message: string }

function sameBalances(a?: Balances, b?: Balances) {
  if (!a || !b) return false
  return LEDGER_BANKS.every((bk) => CCYS.every((c) => (a[bk]?.[c] ?? 0) === (b[bk]?.[c] ?? 0)))
}

function SimTag({ bank }: { bank: string }) {
  if (!SIMULATED_BANKS[bank]) return null
  return <span className="ml-1 text-[10px] font-normal uppercase tracking-wide text-muted">sim</span>
}

/**
 * Liquidity engine: the chaincode nets the selected trades across all four
 * ledger accounts, drops trades a bank cannot fund by a fixed greedy rule,
 * and settles the rest in one transaction, or nothing. Every figure on this
 * screen is read from the chaincode's PreviewLiquidity / LiquiditySettle
 * response; the screen only adds up and formats what the chaincode returned.
 */
export function Liquidity({ online, ledgerBanks }: { online: boolean; ledgerBanks: string[] }) {
  const state = useApi(getState, 10_000)
  const audit = useApi(getAudit, 15_000)
  const scenarios = useApi(getLiquidityScenarios)
  const [picked, setPicked] = useState<string[]>([])
  const [as, setAs] = useState<Party>('BANKIN')
  const [preview, setPreview] = useState<{ ids: string; data?: LiquidityPreview; error?: string; loading: boolean } | null>(null)
  const [outcome, setOutcome] = useState<Outcome>({ kind: 'idle' })
  const [resolving, setResolving] = useState<string | null>(null)

  const matched = useMemo(
    () => (state.data?.trades ?? []).filter((t) => t.status === 'MATCHED').sort((a, b) => a.tradeId.localeCompare(b.tradeId)),
    [state.data],
  )
  const byId = useMemo(() => new Map((state.data?.trades ?? []).map((t) => [t.tradeId, t])), [state.data])
  const selected = picked.filter((id) => byId.get(id)?.status === 'MATCHED' || outcome.kind !== 'idle')
  const key = [...selected].sort().join(',')

  useEffect(() => {
    if (selected.length < 1 || !online) return
    let live = true
    const ids = key.split(',')
    const t = window.setTimeout(async () => {
      setPreview({ ids: key, loading: true })
      const r = await previewLiquidity(ids)
      if (!live) return
      setPreview(r.ok ? { ids: key, data: r.data, loading: false } : { ids: key, error: r.error, loading: false })
    }, 250)
    return () => {
      live = false
      window.clearTimeout(t)
    }
  }, [key, online, selected.length])

  const pv = preview && preview.ids === key && selected.length >= 1 ? preview : null
  const plan = pv?.data?.plan

  function choose(ids: string[]) {
    setOutcome({ kind: 'idle' })
    setPicked(ids)
  }
  function toggle(id: string) {
    choose(picked.includes(id) ? picked.filter((x) => x !== id) : [...picked, id])
  }
  async function onResolveAll() {
    setResolving(null)
    const r = await resolveLiquidity()
    if (!r.ok) return setResolving(r.error)
    if (r.data.truncated) setResolving(`${r.data.matched} matched trades found; the first ${r.data.tradeIds.length} by trade ID are selected (batch limit).`)
    choose(r.data.tradeIds)
  }

  async function onSettle() {
    const ids = key.split(',')
    setOutcome({ kind: 'settling' })
    const r = await liquiditySettle(ids, as)
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
  const canSettle = online && selected.length >= 1 && !!pv?.data?.fundsOk && outcome.kind !== 'settling' && outcome.kind !== 'settled'
  const batches = (audit.data?.log ?? []).filter((e) => e.type === 'LIQUIDITY_SETTLED').sort((a, b) => b.n - a.n)

  return (
    <div className="grid grid-cols-12 gap-6">
      {online && state.error && !state.loading && (
        <div className="col-span-12">
          <ErrorBox title="Could not read ledger state (GET /api/state)" message={state.error} onRetry={() => void state.reload()} />
        </div>
      )}

      <section aria-labelledby="liq-title" className="card card-raised hero col-span-12 p-8 lg:p-10">
        <span className="hero-edge" aria-hidden="true" />
        <div className="max-w-3xl">
          <p className="eyebrow !text-accent-fg">Liquidity engine</p>
          <h2 id="liq-title" className="mt-2 text-balance text-[30px] font-semibold leading-tight tracking-[-0.025em]">
            Net across four ledger accounts and settle what can be funded
          </h2>
          <p className="mt-3 text-[15px] leading-relaxed text-muted">
            The chaincode nets the selected trades across BankIN, BankFX, BankUS and BankSG. If a bank cannot fund its net
            outflow, it drops trades by a fixed greedy rule (greedy, not optimal) and settles the rest in one transaction. If
            no set of trades can be funded, nothing settles.
          </p>
          <p className="mt-3 text-sm text-fg-2">
            <span className="font-semibold">BankUS and BankSG</span>: {SIMULATED_BANK_NOTE.toLowerCase()}, custodied by
            BankIN&apos;s and BankFX&apos;s orgs. They are not orgs and have no peers. Every trade spans both orgs.
          </p>
        </div>

        {online && ledgerBanks.length > 0 && !Object.keys(SIMULATED_BANKS).every((b) => ledgerBanks.includes(b)) && (
          <p className="well mt-6 px-5 py-4 text-sm text-fg-2">
            <span className="font-semibold text-fg">This ledger was initialised with BankIN and BankFX only. </span>
            Netting and gridlock resolution between those two banks still work here; the four-bank scenarios need the
            liquidity-engine deployment (chaincode pvp-le, see the README).
          </p>
        )}

        <ScenarioBar
          scenarios={scenarios.data?.scenarios ?? []}
          error={scenarios.error}
          matched={matched}
          onPick={(ids) => choose(ids)}
          onResolveAll={() => void onResolveAll()}
          online={online}
        />
        {resolving && <p className="mt-3 text-sm text-fg-2">{resolving}</p>}

        <PlanView plan={settled ? undefined : plan} loading={!!pv?.loading} />
        {settled?.batch && <SettledBatch res={settled} />}

        <div className="mt-8 flex flex-wrap items-center justify-between gap-6 border-t border-line pt-6">
          <div className="min-w-0 text-sm">
            {selected.length < 1 ? (
              <p className="text-muted">Pick a scenario, resolve all matched trades, or select trades below.</p>
            ) : pv?.error ? (
              <p className="text-bad">
                <span className="font-semibold">This batch is refused as a whole: </span>
                {plainReason(/ERR_[A-Z_]+/.exec(pv.error)?.[0])}
              </p>
            ) : pv?.data ? (
              pv.data.fundsOk ? (
                <p className="inline-flex items-center gap-2 font-medium text-ok">
                  <IconCheck size={16} />
                  {plan!.droppedTradeIds.length === 0
                    ? 'Every selected trade can settle, net.'
                    : `${plan!.settledTradeIds.length} of ${plan!.inputTradeIds.length} trades can settle; ${plan!.droppedTradeIds.length} would be left MATCHED.`}
                </p>
              ) : (
                <p className="font-medium text-bad">Gridlocked: no set of these trades can be funded together. Settling would move nothing.</p>
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
                  <input type="radio" name="liq-as" className="sr-only" checked={as === p} onChange={() => setAs(p)} />
                  As {bankLabel(p)}
                </label>
              ))}
            </fieldset>
            <Button size="lg" onClick={() => void onSettle()} disabled={!canSettle} loading={outcome.kind === 'settling'}>
              <IconLock size={18} />
              {outcome.kind === 'settling' ? 'Settling…' : 'Settle resolvable set'}
            </Button>
          </div>
        </div>
        {(pv?.error || (pv?.data && !pv.data.fundsOk)) && (
          <details className="mt-3 text-sm">
            <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Technical details</summary>
            <p className="mt-2 break-words font-mono text-xs text-fg-2">{pv?.error ?? pv?.data?.fundsMessage}</p>
          </details>
        )}

        <div aria-live="polite">
          {outcome.kind === 'settled' && (
            <SettledPanel
              result={outcome.res.result}
              headline={`Settled: ${outcome.res.batch?.tradeIds.length ?? 0} trades in one transaction`}
            >
              <p className="mt-2 text-[15px] text-fg">
                The settled trades, every balance and the batch record were written together.
                {(outcome.res.batch?.liquidity?.droppedTradeIds.length ?? 0) > 0 &&
                  ` Dropped trades (${outcome.res.batch!.liquidity!.droppedTradeIds.join(', ')}) were not written and stay MATCHED.`}
              </p>
            </SettledPanel>
          )}
          {outcome.kind === 'rejected' && (
            <RejectedPanel result={outcome.res.result} reread={outcome.reread} headline="Refused: nothing moved, balances unchanged" />
          )}
          {outcome.kind === 'error' && (
            <div className="mt-6">
              <ErrorBox title="The gateway did not return a settlement outcome" message={outcome.message} />
            </div>
          )}
        </div>
      </section>

      <Card
        className="col-span-12 xl:col-span-7"
        title="Matched trades"
        subtitle={`${matched.length} on the ledger`}
        labelledBy="liq-pick"
        action={
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="secondary" size="sm" onClick={() => choose([])} disabled={picked.length === 0}>
              Clear
            </Button>
            <Button variant="secondary" size="sm" onClick={() => void state.reload()} loading={state.refreshing && !!state.data} ariaLabel="Re-read trades">
              <IconRefresh size={14} />
            </Button>
          </div>
        }
      >
        {!state.data ? (
          <div className="space-y-3" aria-hidden="true">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : matched.length === 0 ? (
          <Empty>No matched trades. Load the demo trades with: pvpctl seed (see the README).</Empty>
        ) : (
          <ul className="scroll-thin max-h-[30rem] divide-y divide-line overflow-auto">
            {matched.map((t) => (
              <TradeRow
                key={t.tradeId}
                t={t}
                checked={picked.includes(t.tradeId)}
                fate={plan ? (plan.droppedTradeIds.includes(t.tradeId) ? 'dropped' : plan.settledTradeIds.includes(t.tradeId) ? 'settles' : undefined) : undefined}
                onToggle={() => toggle(t.tradeId)}
              />
            ))}
          </ul>
        )}
      </Card>

      <Card className="col-span-12 xl:col-span-5" title="Liquidity batches settled" subtitle="From the append-only audit log" labelledBy="liq-batches">
        {!audit.data ? (
          <div className="space-y-3" aria-hidden="true">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12" />
            ))}
          </div>
        ) : batches.length === 0 ? (
          <Empty>No liquidity batches yet.</Empty>
        ) : (
          <ol className="scroll-thin max-h-[30rem] divide-y divide-line overflow-auto">
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

function ScenarioBar({
  scenarios,
  error,
  matched,
  onPick,
  onResolveAll,
  online,
}: {
  scenarios: { id: string; title: string; note: string; tradeIds: string[] }[]
  error: string | null
  matched: Trade[]
  onPick: (ids: string[]) => void
  onResolveAll: () => void
  online: boolean
}) {
  const open = new Set(matched.map((t) => t.tradeId))
  return (
    <div className="mt-8">
      <p className="eyebrow">Demo scenarios (seed data)</p>
      <div className="mt-3 flex flex-wrap gap-2">
        {scenarios.map((s) => {
          const ids = s.tradeIds.filter((id) => open.has(id))
          return (
            <Button key={s.id} variant="secondary" size="sm" disabled={!online || ids.length === 0} onClick={() => onPick(ids)}>
              <span title={s.note}>{s.title}</span>
              <Chip className="!h-5 !px-2 text-[10px]">{ids.length === s.tradeIds.length ? `${ids.length}` : `${ids.length}/${s.tradeIds.length}`}</Chip>
            </Button>
          )
        })}
        <Button size="sm" disabled={!online} onClick={onResolveAll}>
          <IconArrows size={14} /> Resolve all matched trades
        </Button>
      </div>
      {error && <p className="mt-2 text-xs text-muted">Scenarios unavailable: {error}</p>}
      {scenarios.some((s) => s.tradeIds.some((id) => !open.has(id))) && (
        <p className="mt-2 text-xs text-muted">A count below the total means some of that scenario&apos;s trades are not MATCHED (already settled, or not seeded).</p>
      )}
    </div>
  )
}

function TradeRow({ t, checked, fate, onToggle }: { t: Trade; checked: boolean; fate?: 'settles' | 'dropped'; onToggle: () => void }) {
  return (
    <li>
      <label className={cx('flex cursor-pointer items-center gap-4 rounded-xl px-2 py-3 transition-colors hover:bg-surface-2', checked && 'bg-accent-soft hover:bg-accent-soft')}>
        <input type="checkbox" checked={checked} onChange={onToggle} className="h-4 w-4 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2">
            <span className="font-mono text-[13px] font-semibold">{t.tradeId}</span>
            {checked && fate === 'settles' && <Chip tone="ok" className="!h-5 !px-2 text-[10px]">settles</Chip>}
            {checked && fate === 'dropped' && <Chip tone="warn" className="!h-5 !px-2 text-[10px]">dropped</Chip>}
          </span>
          <span className="mt-1 block text-xs text-muted">
            {bankLabel(t.usdDeliverer)}
            <SimTag bank={t.usdDeliverer} /> pays USD · {bankLabel(t.inrDeliverer)}
            <SimTag bank={t.inrDeliverer} /> pays INR
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

/** The chaincode's plan: gross vs net, net payments, removals, cycles. */
function PlanView({ plan, loading }: { plan?: LiquidityPlan; loading: boolean }) {
  if (loading) return <Skeleton className="mt-8 h-40" />
  if (!plan) return null
  const np = plan.netPlan
  return (
    <div className="mt-8 space-y-6">
      {np && (
        <div className="grid gap-4 sm:grid-cols-2">
          {CCYS.map((c) => {
            const gross = np.gross[c] ?? 0
            const net = netTotal(np.net, c)
            return (
              <div key={c} className="well px-5 py-4">
                <p className="eyebrow">{c}: gross vs net (settling set)</p>
                <div className="mt-2 flex items-baseline justify-between gap-4">
                  <div className="text-sm">
                    <p className="text-muted">
                      Gross <Money minor={gross} ccy={c} size="sm" />
                    </p>
                    <p className="mt-1">
                      Net <Money minor={net} ccy={c} size="sm" />
                    </p>
                  </div>
                  <p className="tnum text-[36px] font-semibold leading-none tracking-[-0.03em] text-accent-fg">
                    {reductionPct(gross, net)}
                    <span className="text-lg">%</span>
                  </p>
                </div>
                <p className="mt-2 text-xs text-muted">Share of the gross that does not need to move.</p>
              </div>
            )
          })}
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-3">
        <div>
          <p className="eyebrow">Net payments</p>
          {!np ? (
            <p className="mt-2 text-sm text-bad">None: gridlocked, nothing would move.</p>
          ) : np.net.every((n) => n.amount === 0) ? (
            <p className="mt-2 text-sm text-fg-2">Everything offsets: no balance moves.</p>
          ) : (
            <ul className="mt-2 space-y-1.5 text-sm">
              {np.net
                .filter((n) => n.amount > 0)
                .map((n, i) => (
                  <li key={i}>
                    {bankLabel(n.from!)} → {bankLabel(n.to!)} <span className="tnum font-semibold">{money(n.amount, n.currency)}</span>
                  </li>
                ))}
            </ul>
          )}
        </div>
        <div>
          <p className="eyebrow">Gridlock resolution</p>
          {plan.removals.length === 0 ? (
            <p className="mt-2 text-sm text-fg-2">No trade needed to be dropped.</p>
          ) : (
            <ol className="mt-2 space-y-1.5 text-sm">
              {plan.removals.map((r) => (
                <li key={r.step}>
                  <span className="font-mono font-semibold">{r.tradeId}</span> dropped: {bankLabel(r.bank)} was short{' '}
                  <span className="tnum">{money(r.shortfall, r.currency)}</span> and pays{' '}
                  <span className="tnum">{money(r.amount, r.currency)}</span> in it.
                </li>
              ))}
            </ol>
          )}
        </div>
        <div>
          <p className="eyebrow">Cycles offset</p>
          {plan.cycles.length === 0 ? (
            <p className="mt-2 text-sm text-fg-2">None in the settling set.</p>
          ) : (
            <ul className="mt-2 space-y-1.5 text-sm">
              {plan.cycles.map((c, i) => (
                <li key={i}>
                  {c.currency}: {c.path.map(bankLabel).join(' → ')} <span className="tnum font-semibold">{money(c.bottleneck, c.currency)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <p className="text-xs text-muted">
        Computed by the chaincode (PreviewLiquidity) from ledger state. Settling re-runs the same computation inside the
        transaction against the balances at that moment.
      </p>
    </div>
  )
}

function SettledBatch({ res }: { res: LiquiditySettleResponse }) {
  const b = res.batch!
  return (
    <dl className="mt-8 grid gap-x-8 gap-y-3 sm:grid-cols-4">
      <Meta k="Batch" v={<span className="font-mono">{b.batchId}</span>} />
      <Meta k="Trades settled" v={<span className="tnum">{b.tradeIds.length}</span>} />
      <Meta k="Trades dropped" v={<span className="tnum">{b.liquidity?.droppedTradeIds.length ?? 0}</span>} />
      <Meta k="Submitted by" v={b.submittedBy} />
    </dl>
  )
}

