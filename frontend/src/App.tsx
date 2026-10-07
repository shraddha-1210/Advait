import { useEffect, useState, type ReactNode } from 'react'
import { GATEWAY_URL, getHealth } from './api/client'
import { ErrorBoundary } from './components/ErrorBoundary'
import { IconAlert, IconArrows, IconBook, IconCheck, IconLayers, IconMoon, IconScale, IconShield, IconSun, IconX } from './components/Icons'
import { cx } from './lib/cx'
import { bankLabel } from './lib/format'
import { useApi } from './lib/useApi'
import { Audit } from './screens/Audit'
import { Liquidity } from './screens/Liquidity'
import { Netting } from './screens/Netting'
import { Security } from './screens/Security'
import { Settlement } from './screens/Settlement'

type Tab = 'settlement' | 'netting' | 'liquidity' | 'security' | 'audit'

const TABS: { id: Tab; label: string; title: string; icon: ReactNode }[] = [
  { id: 'settlement', label: 'Settlement', title: 'Atomic settlement', icon: <IconArrows size={18} /> },
  { id: 'netting', label: 'Netting', title: 'Bilateral netting', icon: <IconLayers size={18} /> },
  { id: 'liquidity', label: 'Liquidity', title: 'Liquidity engine', icon: <IconScale size={18} /> },
  { id: 'security', label: 'Security', title: 'Threat rejection', icon: <IconShield size={18} /> },
  { id: 'audit', label: 'Audit', title: 'Regulator audit view', icon: <IconBook size={18} /> },
]

export default function App() {
  const [tab, setTab] = useState<Tab>('settlement')
  // Theme lives in React state only (no browser storage). Starts from the OS preference.
  const [dark, setDark] = useState(() => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false)
  const health = useApi(getHealth, 5_000)
  const online = !!health.data?.ok
  // Banks pinned in this ledger's configuration (two or four), from GetConfig via /api/health.
  const ledgerBanks = Object.keys(health.data?.config?.banks ?? {})

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  }, [dark])

  const current = TABS.find((t) => t.id === tab)!

  return (
    <div className="flex min-h-screen">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-xl focus:bg-accent focus:px-4 focus:py-2 focus:text-white"
      >
        Skip to content
      </a>

      <aside className="sidebar sticky top-0 flex h-screen w-[256px] shrink-0 flex-col border-r border-line bg-sidebar px-4 pb-6 pt-7">
        <div className="flex items-center gap-3 px-2">
          <span className="brand-mark grid h-9 w-9 place-items-center rounded-xl text-[15px] font-bold text-white shadow-sm" aria-hidden="true">
            A
          </span>
          <div className="leading-tight">
            <p className="text-[17px] font-bold tracking-tight text-fg">Advait</p>
            <p className="text-xs font-medium text-muted">PvP settlement layer</p>
          </div>
        </div>

        <p className="eyebrow mt-10 px-3 font-semibold text-muted/80">Views</p>
        <nav aria-label="Main" className="mt-3 flex flex-col gap-1.5">
          {TABS.map((t) => {
            const active = tab === t.id
            return (
              <button
                key={t.id}
                onClick={() => setTab(t.id)}
                aria-current={active ? 'page' : undefined}
                className={cx(
                  'relative flex h-11 items-center gap-3 rounded-xl px-3.5 text-left text-[15px] font-medium transition-all duration-150',
                  active
                    ? 'nav-active'
                    : 'text-muted hover:bg-surface-2 hover:text-fg',
                )}
              >
                {active && <span className="absolute -left-4 top-2.5 h-6 w-[3.5px] rounded-r-full bg-accent" aria-hidden="true" />}
                <span className={cx(active ? 'text-accent' : 'text-muted')}>{t.icon}</span>
                <span className={active ? 'font-semibold text-fg' : undefined}>{t.label}</span>
              </button>
            )
          })}
        </nav>

        <div className="mt-auto space-y-4 px-1">
          <div className="border-t border-line pt-4 text-xs leading-relaxed text-muted">
            Simulated tokenized cash on a single Drunix network. Demonstration, not a production system.
          </div>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 border-b border-line bg-surface/95 px-10 py-5 backdrop-blur-xl shadow-[0_2px_10px_rgba(76,29,149,0.05)]">
          <div className="flex flex-wrap items-center gap-x-8 gap-y-3">
            <div className="min-w-0 flex-1">
              <p className="eyebrow !text-accent-fg">Advait · INR / USD</p>
              <h1 className="mt-1 text-[26px] font-bold leading-tight tracking-[-0.025em] text-fg">{current.title}</h1>
            </div>
            <HealthPill loading={health.loading} online={online} parties={health.data?.parties} />
            <button
              onClick={() => setDark((d) => !d)}
              aria-label={dark ? 'Switch to light theme' : 'Switch to dark theme'}
              title={dark ? 'Light theme' : 'Dark theme'}
              className="card grid h-10 w-10 place-items-center !rounded-full text-muted transition-colors hover:text-fg hover:border-hairline-strong"
            >
              {dark ? <IconSun size={17} /> : <IconMoon size={17} />}
            </button>
          </div>
          <ProblemStrip />
        </header>

        {!health.loading && !online && (
          <div role="alert" className="mx-10 mt-6 flex items-center gap-3 rounded-xl bg-bad-soft px-4 py-3 text-sm text-bad ring-1 ring-inset ring-bad-line">
            <IconAlert size={16} className="shrink-0" />
            <p className="min-w-0">
              <span className="font-semibold">Gateway offline.</span>{' '}
              <span className="text-fg-2">
                Actions are disabled and figures are not shown. Expected at <span className="font-mono">{GATEWAY_URL}</span>; retrying every 5 s.
              </span>
              {health.error && <span className="mt-0.5 block truncate font-mono text-xs opacity-80">{health.error}</span>}
            </p>
          </div>
        )}

        <main id="main" key={tab} className="anim-rise flex-1 px-10 py-8">
          <ErrorBoundary key={tab} name={current.label}>
            {tab === 'settlement' && <Settlement online={online} ledgerBanks={ledgerBanks} />}
            {tab === 'netting' && <Netting online={online} />}
            {tab === 'liquidity' && <Liquidity online={online} ledgerBanks={ledgerBanks} />}
            {tab === 'security' && <Security online={online} />}
            {tab === 'audit' && <Audit online={online} />}
          </ErrorBoundary>
        </main>
      </div>
    </div>
  )
}

function HealthPill({ loading, online, parties }: { loading: boolean; online: boolean; parties?: Record<string, string> }) {
  return (
    <span className="card inline-flex h-10 items-center gap-2.5 !rounded-full px-4 text-[13px] font-semibold">
      <span
        className={cx(
          'relative h-2 w-2 rounded-full',
          loading ? 'bg-muted' : online ? 'ping-dot bg-ok text-ok' : 'bg-bad',
        )}
        aria-hidden="true"
      />
      <span className={loading ? 'text-muted' : online ? 'text-fg' : 'text-bad'}>
        {loading ? 'Checking gateway' : online ? 'Gateway online' : 'Gateway offline'}
      </span>
      {online && parties && (
        <span className="hidden border-l border-line pl-2.5 font-medium text-muted xl:inline">
          {Object.entries(parties)
            .map(([b, m]) => `${bankLabel(b)} ${m}`)
            .join(' · ')}
        </span>
      )}
    </span>
  )
}

/** The problem and the guarantee, in plain words, on every screen. */
function ProblemStrip() {
  return (
    <div className="card mt-4 grid overflow-hidden !rounded-2xl text-[13px] leading-relaxed md:grid-cols-2">
      <p className="flex items-start gap-3 px-5 py-3.5 text-fg-2">
        <span className="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full bg-bad-soft text-bad ring-1 ring-inset ring-bad-line">
          <IconX size={11} />
        </span>
        <span>
          <span className="font-semibold text-fg">The problem. </span>
          Two banks settle an FX trade in separate steps, so if one pays and the other fails, the first loses the money.
        </span>
      </p>
      <p className="flex items-start gap-3 border-t border-line px-5 py-3.5 text-fg-2 md:border-l md:border-t-0">
        <span className="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full bg-ok-soft text-ok ring-1 ring-inset ring-ok-line">
          <IconCheck size={11} />
        </span>
        <span>
          <span className="font-semibold text-fg">What Advait does. </span>
          Settles both legs together in one ledger transaction, or neither. Nobody pays without being paid.
        </span>
      </p>
    </div>
  )
}
