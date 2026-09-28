import { useMemo, useState, type ReactNode } from 'react'
import { getAttacks, getRejections, getState, runAttack } from '../api/client'
import type { AttackInfo, AttackReport, State } from '../api/types'
import { IconAlert, IconCheck, IconScale, IconShield, IconShieldX, IconX } from '../components/Icons'
import { Money } from '../components/Money'
import { Button, Card, Chip, Empty, ErrorBox, Skeleton } from '../components/ui'
import { cx } from '../lib/cx'
import { bankLabel, timeOf } from '../lib/format'
import { REASON } from '../lib/reasons'
import { useApi, type ApiState } from '../lib/useApi'

// Presentation only: how catalogue entries are grouped on screen. Anything the
// gateway adds later that is not listed here lands in "Other".
const GROUPS: { title: string; names: string[] }[] = [
  {
    title: 'Instruction and settlement attacks',
    names: ['double-settle', 'replay-instruction', 'unilateral-instruction', 'forged-instruction', 'mismatched-instruction', 'underfunded'],
  },
  { title: 'FX rate attacks', names: ['unsigned-rate', 'tampered-rate', 'fake-oracle', 'stale-rate', 'out-of-band-rate'] },
  { title: 'Amount and supply attacks', names: ['negative-amount', 'overflow-amount', 'reinit'] },
  { title: 'Endorsement and network', names: ['unilateral-endorsement', 'bank-offline'] },
]


type Run = { status: 'running' } | { status: 'done'; report: AttackReport } | { status: 'error'; error: string }

export function Security({ online }: { online: boolean }) {
  const catalogue = useApi(getAttacks)
  const rejections = useApi(getRejections, 3_000)
  const state = useApi(getState, 5_000)
  const [runs, setRuns] = useState<Record<string, Run>>({})
  const [last, setLast] = useState<string | null>(null)
  const anyRunning = Object.values(runs).some((r) => r.status === 'running')

  async function attack(name: string) {
    setLast(name)
    setRuns((r) => ({ ...r, [name]: { status: 'running' } }))
    const res = await runAttack(name)
    setRuns((r) => ({ ...r, [name]: res.ok ? { status: 'done', report: res.data } : { status: 'error', error: res.error } }))
    void rejections.reload()
    void state.reload()
  }

  const grouped = useMemo(() => {
    const list = catalogue.data ?? []
    const known = new Set(GROUPS.flatMap((g) => g.names))
    const out = GROUPS.map((g) => ({ title: g.title, items: g.names.map((n) => list.find((a) => a.name === n)).filter((a): a is AttackInfo => !!a) }))
    const other = list.filter((a) => !known.has(a.name))
    if (other.length) out.push({ title: 'Other', items: other })
    return out.filter((g) => g.items.length > 0)
  }, [catalogue.data])

  const lastRun = last ? runs[last] : undefined
  const lastInfo = last ? catalogue.data?.find((a) => a.name === last) : undefined
  // The gateway sends null, not [], before its first refusal.
  const feed = rejections.data ? (rejections.data.rejections ?? []) : undefined
  const refusedCount = feed?.length

  return (
    <div className="grid grid-cols-12 gap-6">
      <section aria-labelledby="sec-title" className="card card-raised hero col-span-12 p-8 lg:p-10 xl:col-span-8">
      <span className="hero-edge" aria-hidden="true" />
        <div className="flex flex-wrap items-start justify-between gap-6">
          <div className="min-w-0 max-w-lg flex-1">
            <p className="eyebrow !text-accent-fg">Hostile participant model</p>
            <h2 id="sec-title" className="mt-2 text-balance text-[30px] font-semibold leading-tight tracking-[-0.025em]">
              Deterministic rejection of a defined threat set
            </h2>
            <p className="mt-3 text-[15px] leading-relaxed text-muted">
              Each attack runs for real against the live network, most as a hostile BankFX with valid credentials. The verdict
              below is exactly what the chaincode or the network returned.
            </p>
          </div>
          <dl className="well grid shrink-0 grid-cols-2 gap-8 px-6 py-5">
            <div>
              <dt className="eyebrow">Threats</dt>
              <dd className="tnum mt-1 text-[28px] font-semibold leading-none tracking-tight">
                {catalogue.data ? catalogue.data.length : <Skeleton className="mt-1 h-7 w-10" />}
              </dd>
            </div>
            <div>
              <dt className="eyebrow">Refused</dt>
              <dd className="tnum mt-1 text-[28px] font-semibold leading-none tracking-tight">
                {refusedCount !== undefined ? refusedCount : <Skeleton className="mt-1 h-7 w-10" />}
              </dd>
            </div>
          </dl>
        </div>

        <div aria-live="polite" className="mt-8">
          {!lastRun ? (
            <div className="well flex items-center gap-6 p-6">
              <div className="orb !h-16 !w-16 shrink-0" data-state={online ? 'idle' : 'offline'}>
                <IconShield size={24} />
              </div>
              <div>
                <p className="text-[15px] font-semibold">Awaiting an attack</p>
                <p className="mt-1 text-sm text-muted">Choose one from the catalogue below. Its real verdict appears here.</p>
              </div>
            </div>
          ) : lastRun.status === 'running' ? (
            <div className="well p-6" aria-busy="true">
              <p className="text-[15px] font-semibold">Running {lastInfo?.threat ?? last}…</p>
              <p className="mt-1 text-sm text-muted">
                {last === 'bank-offline'
                  ? 'Stopping BankFX peer, attempting settlement, restarting the peer. This takes about 25 seconds.'
                  : 'Submitting to both banks’ peers.'}
              </p>
              <Skeleton className="mt-6 h-7 w-64" />
              <Skeleton className="mt-3 h-4 w-full" />
              <Skeleton className="mt-2 h-4 w-3/4" />
            </div>
          ) : lastRun.status === 'error' ? (
            <ErrorBox title="The attack could not be run (gateway error, not a chaincode verdict)" message={lastRun.error} />
          ) : (
            <Verdict report={lastRun.report} />
          )}
        </div>
      </section>

      <ConservationCard className="col-span-12 xl:col-span-4" state={state} online={online} />

      <Card className="col-span-12" title="Attack catalogue" subtitle="One entry per threat-matrix row" labelledBy="catalogue">
        {online && catalogue.error && !catalogue.loading && (
          <div className="mb-6">
            <ErrorBox title="Could not read /api/attacks" message={catalogue.error} onRetry={() => void catalogue.reload()} />
          </div>
        )}
        {!catalogue.data ? (
          <div className="space-y-8" aria-hidden="true">
            {[3, 3].map((n, gi) => (
              <div key={gi}>
                <Skeleton className="mb-4 h-3 w-40" />
                <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                  {Array.from({ length: n }, (_, i) => (
                    <div key={i} className="card p-5">
                      <Skeleton className="h-4 w-2/3" />
                      <Skeleton className="mt-2 h-3 w-1/3" />
                      <Skeleton className="mt-5 h-3 w-full" />
                      <Skeleton className="mt-2 h-3 w-5/6" />
                      <div className="mt-6 flex justify-between">
                        <Skeleton className="h-7 w-36 !rounded-full" />
                        <Skeleton className="h-8 w-24" />
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        ) : grouped.length === 0 ? (
          <Empty>The gateway returned no attacks.</Empty>
        ) : (
          <div className="space-y-10">
            {grouped.map((g) => (
              <div key={g.title}>
                <h3 className="eyebrow mb-4 flex items-center gap-2">
                  {g.title}
                  <span className="tnum text-muted/70">{g.items.length}</span>
                </h3>
                <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                  {g.items.map((a) => (
                    <AttackButton key={a.name} info={a} run={runs[a.name]} disabled={!online || anyRunning} onRun={() => void attack(a.name)} />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card
        className="col-span-12"
        title="Refused attempts"
        subtitle="The gateway's in-memory log, not ledger data"
        labelledBy="feed"
        action={<Chip>Refreshes every 3 s</Chip>}
      >
        {!rejections.data ? (
          <>
            {online && rejections.error && !rejections.loading && (
              <div className="mb-4">
                <ErrorBox title="Could not read /api/rejections" message={rejections.error} />
              </div>
            )}
            <div className="divide-y divide-line" aria-hidden="true">
              {[0, 1, 2].map((i) => (
                <div key={i} className="flex items-center gap-4 py-4">
                  <Skeleton className="h-8 w-8 !rounded-full" />
                  <div className="flex-1">
                    <Skeleton className="h-4 w-56" />
                    <Skeleton className="mt-2 h-3 w-2/3" />
                  </div>
                  <Skeleton className="h-3 w-24" />
                </div>
              ))}
            </div>
          </>
        ) : (
          <>
            <p className="mb-4 text-[13px] leading-relaxed text-muted">{rejections.data.note}</p>
            {!feed || feed.length === 0 ? (
              <Empty>No refused attempts since the gateway started.</Empty>
            ) : (
              <ol className="scroll-thin max-h-[28rem] divide-y divide-line overflow-auto">
                {feed
                  .slice()
                  .reverse()
                  .map((r, i) => (
                    <li key={`${r.at}-${i}`} className="flex items-start gap-4 py-4">
                      <span className="mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-full bg-bad-soft text-bad ring-1 ring-inset ring-bad-line">
                        <IconX size={14} />
                      </span>
                      <div className="min-w-0 flex-1">
                        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
                          <span className="font-mono font-semibold text-bad">{r.outcome.code}</span>
                          <span className="text-muted">
                            at {r.outcome.stage} · as {bankLabel(r.outcome.party)} ({r.outcome.mspId})
                          </span>
                          {r.attack && <Chip>{r.attack}</Chip>}
                        </p>
                        <p className="mt-1 text-sm text-fg-2">{r.attempt}</p>
                        {r.outcome.message && <p className="mt-1 break-words font-mono text-xs text-muted">{r.outcome.message}</p>}
                      </div>
                      <span className="shrink-0 text-xs text-muted">{timeOf(r.at)}</span>
                    </li>
                  ))}
              </ol>
            )}
          </>
        )}
      </Card>
    </div>
  )
}

function AttackButton({ info, run, disabled, onRun }: { info: AttackInfo; run?: Run; disabled: boolean; onRun: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const disruptive = info.name === 'bank-offline'
  const status: ReactNode =
    run?.status === 'running' ? (
      <Chip tone="warn">Running</Chip>
    ) : run?.status === 'error' ? (
      <Chip tone="bad" icon={<IconAlert size={12} />}>Gateway error</Chip>
    ) : run?.status === 'done' ? (
      run.report.refused ? (
        <Chip tone="bad" icon={<IconShieldX size={12} />}>Rejected</Chip>
      ) : (
        <Chip tone="warn" icon={<IconAlert size={12} />}>Not refused</Chip>
      )
    ) : null
  return (
    <div className={cx('card card-lift flex flex-col p-5', run?.status === 'done' && run.report.refused && 'ring-4 ring-bad-soft')}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-[15px] font-semibold leading-snug">{info.threat}</p>
          <p className="mt-0.5 font-mono text-xs text-muted">{info.name}</p>
        </div>
        {status}
      </div>
      <p className="mt-3 flex-1 text-[13px] leading-relaxed text-muted">{info.description}</p>
      <p className="mt-3 text-xs text-muted">
        Actor: <span className="font-medium text-fg-2">{info.actor}</span>
      </p>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-4">
        <span className="font-mono text-[11px] text-muted" title={`Expected refusal at ${info.stage}`}>
          {info.expect}
        </span>
        {disruptive && confirm ? (
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={() => setConfirm(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              size="sm"
              onClick={() => {
                setConfirm(false)
                onRun()
              }}
              disabled={disabled}
            >
              Stop peer and run
            </Button>
          </div>
        ) : (
          <Button variant="secondary" size="sm" onClick={() => (disruptive ? setConfirm(true) : onRun())} disabled={disabled} loading={run?.status === 'running'}>
            {run?.status === 'running' ? 'Running…' : 'Run attack'}
          </Button>
        )}
      </div>
      {disruptive && <p className="mt-2 text-xs text-warn">Stops a real peer container for about 25 s.</p>}
    </div>
  )
}

function Verdict({ report }: { report: AttackReport }) {
  const o = report.result?.outcome
  if (!report.refused || !o) {
    return (
      <div className="rounded-2xl bg-warn-soft p-6 text-warn ring-1 ring-inset ring-warn-line">
        <p className="flex items-center gap-2 text-lg font-semibold">
          <IconAlert size={20} /> Not refused
        </p>
        <p className="mt-2 text-sm text-fg-2">The gateway reports this attempt was not refused. That is a defence failure and should be investigated.</p>
      </div>
    )
  }
  const reason = o.code ? REASON[o.code] : undefined
  const moved = report.result?.balancesMoved
  const inv = report.result?.after?.invariantHolds
  return (
    <div className="anim-shake overflow-hidden rounded-2xl bg-bad-soft ring-1 ring-inset ring-bad-line">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-bad-line px-6 py-4">
        <p className="flex items-center gap-2.5 text-[22px] font-semibold tracking-tight text-bad">
          <IconShieldX size={24} /> Rejected
        </p>
        {report.expectedCode ? (
          <Chip tone="ok" icon={<IconCheck size={13} />}>Refused by the intended defence</Chip>
        ) : (
          <Chip tone="warn" icon={<IconAlert size={13} />}>Unexpected code (expected {report.expect})</Chip>
        )}
      </div>
      <div className="p-6">
        <p className="break-words font-mono text-lg font-semibold text-bad">{o.code}</p>
        {reason && <p className="mt-1.5 text-[15px] font-medium text-fg">{reason}</p>}
        <p className="mt-4 break-words rounded-xl bg-surface/80 p-3 font-mono text-[13px] leading-relaxed text-fg-2 ring-1 ring-inset ring-line">{o.message}</p>
        <dl className="mt-5 grid gap-x-8 gap-y-4 text-sm sm:grid-cols-3">
          <Pair k="Threat" v={report.threat} />
          <Pair k="Refused at" v={o.stage ?? 'n/a'} />
          <Pair k="Submitted as" v={`${bankLabel(o.party)} (${o.mspId})`} />
          <Pair
            k="Balances moved"
            v={
              moved === undefined ? (
                'n/a'
              ) : moved ? (
                <span className="font-semibold text-bad">Yes</span>
              ) : (
                <span className="inline-flex items-center gap-1 font-semibold text-ok">
                  <IconCheck size={14} /> No
                </span>
              )
            }
          />
          <Pair
            k="Value conserved after"
            v={
              inv === undefined ? (
                'n/a'
              ) : inv ? (
                <span className="inline-flex items-center gap-1 font-semibold text-ok">
                  <IconCheck size={14} /> Holds
                </span>
              ) : (
                <span className="font-semibold text-bad">Does not hold</span>
              )
            }
          />
          {o.blockNumber ? <Pair k="Block (invalidated tx)" v={String(o.blockNumber)} /> : null}
        </dl>
        <p className="mt-5 text-sm text-fg-2">
          <span className="font-semibold text-fg">Attempt: </span>
          {report.attempt}
        </p>
        {report.note && <p className="mt-1 text-sm text-muted">{report.note}</p>}
        <details className="mt-4 text-sm">
          <summary className="cursor-pointer font-semibold text-muted hover:text-fg">Exact chaincode arguments ({report.payload?.length ?? 0})</summary>
          <pre className="scroll-thin mt-2 max-h-48 overflow-auto rounded-xl bg-surface p-3 font-mono text-xs text-fg-2 ring-1 ring-inset ring-line">
            {JSON.stringify(report.payload, null, 2)}
          </pre>
          {(report.setup?.length ?? 0) > 0 && (
            <p className="mt-2 text-muted">
              {report.setup!.length} legitimate setup transaction{report.setup!.length === 1 ? '' : 's'} committed first (for example, a
              matched trade to attack).
            </p>
          )}
        </details>
      </div>
    </div>
  )
}

function Pair({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="eyebrow">{k}</dt>
      <dd className="mt-1 break-words font-medium">{v}</dd>
    </div>
  )
}

function ConservationCard({ className, state, online }: { className?: string; state: ApiState<State>; online: boolean }) {
  const snap = state.data?.snapshot
  return (
    <section aria-labelledby="conservation" className={cx('card flex flex-col p-6 lg:p-7', className)}>
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 id="conservation" className="text-[17px] font-semibold tracking-tight">
            Value conservation
          </h2>
          <p className="mt-0.5 text-[13px] text-muted">Invariant enforced in chaincode</p>
        </div>
        {snap ? (
          snap.invariantHolds ? (
            <Chip tone="ok" icon={<IconScale size={13} />}>Conserved</Chip>
          ) : (
            <Chip tone="bad" icon={<IconAlert size={13} />}>Not conserved</Chip>
          )
        ) : (
          <Skeleton className="h-7 w-28 !rounded-full" />
        )}
      </div>

      {online && state.error && !state.loading && (
        <div className="mt-5">
          <ErrorBox title="Could not read /api/state" message={state.error} />
        </div>
      )}

      <div className="mt-6 space-y-4">
        {(snap?.conservation ?? [{ currency: 'INR' }, { currency: 'USD' }]).map((c) => {
          const r = 'supply' in c ? c : undefined
          return (
            <div key={c.currency} className="well p-4">
              <div className="flex items-center justify-between">
                <p className="eyebrow">{c.currency}</p>
                {r ? (
                  r.holds ? (
                    <span className="inline-flex items-center gap-1 text-xs font-semibold text-ok">
                      <IconCheck size={13} /> Sum equals supply
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 text-xs font-semibold text-bad">
                      <IconX size={13} /> Mismatch
                    </span>
                  )
                ) : null}
              </div>
              <dl className="mt-3 space-y-2 text-sm">
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-muted">Supply at init</dt>
                  <dd>{r ? <Money minor={r.supply} ccy={r.currency} size="sm" /> : <Skeleton className="h-4 w-32" />}</dd>
                </div>
                <div className="flex items-baseline justify-between gap-3">
                  <dt className="text-muted">Sum of balances</dt>
                  <dd>{r ? <Money minor={r.sum} ccy={r.currency} size="sm" /> : <Skeleton className="h-4 w-32" />}</dd>
                </div>
              </dl>
            </div>
          )
        })}
      </div>
      <p className="mt-auto border-t border-line pt-4 text-xs leading-relaxed text-muted">
        {snap ? <>The chaincode's live report, read {timeOf(snap.readAt)}. Checked after every settlement.</> : 'Checked after every settlement. Awaiting a ledger read.'}
      </p>
    </section>
  )
}
