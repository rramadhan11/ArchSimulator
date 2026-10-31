import { ReactFlowProvider } from '@xyflow/react'
import { useRef, useState } from 'react'
import { Canvas } from './canvas/Canvas'
import { Inspector } from './panels/Inspector'
import { Palette } from './panels/Palette'
import { QuickCheck } from './panels/QuickCheck'
import { TimelineView } from './panels/TimelineView'
import { latest, savedDesign, useDesign } from './store/design'
import { encodeShare, parseSaved } from './store/persist'
import { templates } from './store/templates'
import { Button, cx, fmt, verdictStyle } from './ui/kit'

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
      <select
        className="h-7 rounded-md border border-line bg-panel px-2 text-[12px]"
        value=""
        onChange={(e) => {
          const t = templates.find((x) => x.id === e.target.value)
          if (t && confirm(`Replace the current design with “${t.title}”? Export it first if you want to keep it.`)) {
            useDesign.getState().load(t.design())
            history.replaceState(null, '', location.pathname)
          }
        }}
      >
        <option value="">New from template…</option>
        {templates.map((t) => (
          <option key={t.id} value={t.id}>
            {t.title}
          </option>
        ))}
      </select>
      <div className="ml-auto flex items-center gap-1.5">
        {note && <span className="mr-2 text-[11.5px] text-muted">{note}</span>}
        <input ref={fileInput} type="file" accept="application/json,.json" className="hidden" onChange={(e) => e.target.files?.[0] && void importFile(e.target.files[0])} />
        <Button variant="ghost" onClick={() => fileInput.current?.click()}>
          Import
        </Button>
        <Button variant="ghost" onClick={exportFile}>
          Export
        </Button>
        <Button onClick={() => void share()}>Share link</Button>
        <form method="post" action="/logout">
          <Button type="submit" variant="ghost" title="Sign out">
            Sign out
          </Button>
        </form>
      </div>
    </header>
  )
}

type Tab = 'check' | 'timeline'

function Workspace() {
  const [tab, setTab] = useState<Tab>('check')
  const [open, setOpen] = useState(true)
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
          <button type="button" className="ml-auto px-2 text-[11px] text-muted hover:text-fg" onClick={() => setOpen(!open)}>
            {open ? 'Hide ▾' : 'Show ▴'}
          </button>
        </div>
        {open && <div className="min-h-0 flex-1 overflow-y-auto">{tab === 'check' ? <QuickCheck /> : <TimelineView />}</div>}
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
