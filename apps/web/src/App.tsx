import { ReactFlowProvider } from '@xyflow/react'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Canvas } from './canvas/Canvas'
import { DesignsDialog, type DesignsTab } from './panels/DesignsDialog'
import { Inspector } from './panels/Inspector'
import { RatesDialog } from './panels/Cost'
import { Methodology } from './panels/Methodology'
import { Palette } from './panels/Palette'
import { QuickCheck } from './panels/QuickCheck'
import { designKey, latest, savedDesign, useDesign } from './store/design'
import { buildReport } from './report/report'
import { encodeShare, parseSaved } from './store/persist'
import { Button, cx, fmt, verdictStyle } from './ui/kit'

// The charts (ECharts) are most of the bundle and only the time simulation uses them.
const TimelineView = lazy(() => import('./panels/TimelineView').then((m) => ({ default: m.TimelineView })))

export function App() {
  return (
    <ReactFlowProvider>
      <div className="grid h-full grid-rows-[44px_1fr]">
        <TopBar />
        <div className="grid min-h-0 grid-cols-[220px_1fr_340px]">
          <Palette />
          <Workspace />
          <Inspector />
        </div>
      </div>
    </ReactFlowProvider>
  )
}

function TopBar() {
  const name = useDesign((s) => s.name)
  const fileInput = useRef<HTMLInputElement>(null)
  const [note, setNote] = useState<string>()
  const [dialog, setDialog] = useState<DesignsTab | null>(null)
  const [method, setMethod] = useState(false)
  const [rates, setRates] = useState(false)
  const flash = (msg: string) => {
    setNote(msg)
    setTimeout(() => setNote(undefined), 2500)
  }

  const exportFile = () => {
    const d = savedDesign()
    const blob = new Blob([JSON.stringify(d.architecture, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${d.name.replace(/[^\w.-]+/g, '-').toLowerCase() || 'design'}.json`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const importFile = async (file: File) => {
    try {
      const d = parseSaved(await file.text())
      useDesign.getState().load({ ...d, name: file.name.replace(/\.json$/i, '') })
      flash('Imported')
    } catch (e) {
      flash(`Import failed: ${e instanceof Error ? e.message : String(e)}`)
    }
  }

  const report = () => {
    const s = useDesign.getState()
    const d = savedDesign()
    const b = s.baseline
    const html = buildReport({
      name: d.name,
      architecture: d.architecture,
      check: latest(s.check),
      timeline: latest(s.timeline),
      baseline: b ? { name: b.saved.name, check: latest(b.check), timeline: latest(b.timeline) } : undefined,
      generatedAt: new Date(),
    })
    const url = URL.createObjectURL(new Blob([html], { type: 'text/html' }))
    window.open(url, '_blank', 'noopener')
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
    if (!latest(s.timeline)) flash('Report opened. Run the time simulation first to include it.')
  }

  const share = async () => {
    const hash = await encodeShare(savedDesign())
    const url = `${location.origin}${location.pathname}${hash}`
    history.replaceState(null, '', hash)
    try {
      await navigator.clipboard.writeText(url)
      flash('Share link copied (the design travels in the link; recipients still need to sign in)')
    } catch {
      flash('Share link is in the address bar')
    }
  }

  return (
    <header className="flex items-center gap-3 border-b border-line bg-panel px-3">
      <div className="flex items-center gap-2">
        <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden className="text-accent">
          <path fill="currentColor" d="M4 5h6v4H4zm10 0h6v4h-6zM9 15h6v4H9zM7 9v3h10V9h-2v1H9V9zm4 3h2v3h-2z" />
        </svg>
        <span className="text-[13px] font-bold tracking-tight">ArchSim</span>
      </div>
      <span className="text-line-strong">/</span>
      <span className="max-w-[260px] truncate text-[13px] font-medium" title="Rename under Workload (click the empty canvas)">
        {name}
      </span>
      <SaveButton flash={flash} />
      <Button variant="ghost" onClick={() => setDialog('saved')}>
        Designs
      </Button>
      <Button variant="ghost" onClick={() => setDialog('templates')}>
        New…
      </Button>
      <DesignsDialog open={dialog !== null} tab={dialog ?? 'saved'} onTab={setDialog} onClose={() => setDialog(null)} />
      <div className="ml-auto flex items-center gap-1.5">
        {note && <span className="mr-2 text-[11.5px] text-muted">{note}</span>}
        <input ref={fileInput} type="file" accept="application/json,.json" className="hidden" onChange={(e) => e.target.files?.[0] && void importFile(e.target.files[0])} />
        <Button variant="ghost" onClick={() => fileInput.current?.click()}>
          Import
        </Button>
        <Button variant="ghost" onClick={exportFile}>
          Export
        </Button>
        <Button variant="ghost" onClick={report} title="Printable report of the design and its latest results (print to PDF)">
          Report
        </Button>
        <Button onClick={() => void share()}>Share link</Button>
        <BaselineControl onPinned={() => flash('Pinned as baseline: results now show this version next to your edits')} />
        <Button variant="ghost" onClick={() => setRates(true)} title="Prices per instance hour used for the cost estimate; set your own">
          Rates
        </Button>
        <RatesDialog open={rates} onClose={() => setRates(false)} />
        <Button variant="ghost" onClick={() => setMethod(true)}>
          How it works
        </Button>
        <Methodology open={method} onClose={() => setMethod(false)} />
        <form method="post" action="/logout">
          <Button type="submit" variant="ghost" title="Sign out">
            Sign out
          </Button>
        </form>
      </div>
    </header>
  )
}

function BaselineControl({ onPinned }: { onPinned: () => void }) {
  const baseline = useDesign((s) => s.baseline)
  const { pinBaseline, clearBaseline } = useDesign.getState()
  if (!baseline) {
    return (
      <Button variant="ghost" title="Keep a copy of this version to compare later edits against" onClick={() => (pinBaseline(), onPinned())}>
        Pin baseline
      </Button>
    )
  }
  return (
    <span className="flex h-7 items-center gap-1 rounded-md border border-dashed border-line-strong pr-1 pl-2 text-[11.5px] text-muted">
      vs <span className="max-w-[140px] truncate font-medium text-fg">{baseline.saved.name}</span>
      <button type="button" className="px-1 hover:text-fg" title="Re-pin: make the current design the baseline" onClick={() => (pinBaseline(), onPinned())}>
        ⟳
      </button>
      <button type="button" className="px-1 hover:text-bad" title="Stop comparing" onClick={clearBaseline}>
        ×
      </button>
    </span>
  )
}

/** Saves a new server version; shows whether there are unsaved changes. Ctrl/Cmd+S. */
function SaveButton({ flash }: { flash: (msg: string) => void }) {
  const link = useDesign((s) => s.server)
  const dirty = useDesign((s) => s.server === null || designKey() !== s.server.savedKey)
  const [saving, setSaving] = useState(false)
  const save = async () => {
    if (saving) return
    setSaving(true)
    try {
      await useDesign.getState().saveToServer()
      flash(`Saved as version ${useDesign.getState().server?.version}`)
    } catch (e) {
      flash(`Not saved: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setSaving(false)
    }
  }
  const saveRef = useRef(save)
  saveRef.current = save
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void saveRef.current()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  return (
    <Button
      variant={dirty ? 'default' : 'ghost'}
      onClick={() => void save()}
      disabled={saving}
      title={link ? `Version ${link.version} on the server (Ctrl/Cmd+S saves a new version)` : 'Save to the server with version history (Ctrl/Cmd+S)'}
    >
      {saving ? 'Saving…' : !link ? 'Save' : dirty ? `Save · v${link.version} + edits` : `Saved v${link.version}`}
    </Button>
  )
}

type Tab = 'check' | 'timeline'

function Workspace() {
  const [tab, setTab] = useState<Tab>('check')
  const [open, setOpen] = useState(true)
  const simulating = useDesign((s) => s.timeline.state === 'running')
  const simulate = () => {
    setTab('timeline')
    setOpen(true)
    void useDesign.getState().runTimeline()
  }
  return (
    <div className={cx('grid min-h-0 min-w-0', open ? 'grid-rows-[1fr_minmax(260px,45%)]' : 'grid-rows-[1fr_36px]')}>
      <div className="min-h-0">
        <Canvas />
      </div>
      <div className="flex min-h-0 flex-col border-t border-line bg-panel-2">
        <div className="flex h-9 shrink-0 items-center gap-1 border-b border-line bg-panel px-2">
          <TabButton active={tab === 'check'} onClick={() => (setTab('check'), setOpen(true))}>
            Quick check <CheckBadge />
          </TabButton>
          <TabButton active={tab === 'timeline'} onClick={() => (setTab('timeline'), setOpen(true))}>
            Time simulation
          </TabButton>
          <Button variant="primary" className="ml-auto" onClick={simulate} disabled={simulating} title="Run the time simulation of the workload's load pattern (quick check runs by itself on every edit)">
            {simulating ? 'Simulating…' : '▶ Run simulation'}
          </Button>
          <button type="button" className="px-2 text-[11px] text-muted hover:text-fg" onClick={() => setOpen(!open)}>
            {open ? 'Hide ▾' : 'Show ▴'}
          </button>
        </div>
        {open && (
          <div className="min-h-0 flex-1 overflow-y-auto">
            {tab === 'check' ? (
              <QuickCheck />
            ) : (
              <Suspense fallback={<p className="p-4 text-[12px] text-muted">Loading charts…</p>}>
                <TimelineView />
              </Suspense>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

function TabButton({ active, children, onClick }: { active: boolean; children: React.ReactNode; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cx('flex h-7 items-center gap-2 rounded-md px-2.5 text-[12px] font-medium', active ? 'bg-panel-2 text-fg' : 'text-muted hover:text-fg')}
    >
      {children}
    </button>
  )
}

function CheckBadge() {
  const check = useDesign((s) => s.check)
  const r = latest(check)
  if (check.state === 'running' && !r) return <span className="text-[11px] text-muted">…</span>
  if (check.state === 'error') return <span className="h-2 w-2 rounded-full bg-bad" title="Design error" />
  if (!r) return null
  const v = verdictStyle[r.target.verdict]
  return (
    <span className={cx('num rounded px-1.5 text-[10.5px]', v.cls)}>
      {v.label} · max {r.maxSustainableTps === null ? '∞' : fmt(r.maxSustainableTps)}
    </span>
  )
}
