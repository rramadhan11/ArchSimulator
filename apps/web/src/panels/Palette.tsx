import { useReactFlow } from '@xyflow/react'
import { useMemo, useState } from 'react'
import { archetypeMeta } from '../canvas/archetypes'
import { DRAG_MIME } from '../canvas/Canvas'
import type { Component } from '../engine/types'
import { useDesign } from '../store/design'

export function Palette() {
  const catalog = useDesign((s) => s.catalog)
  const [q, setQ] = useState('')
  const rf = useReactFlow()

  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase()
    const byLabel = new Map<string, { order: number; items: Component[] }>()
    for (const c of Object.values(catalog.components)) {
      if (needle && !`${c.displayName} ${c.id}`.toLowerCase().includes(needle)) continue
      const meta = archetypeMeta[c.archetype]
      const g = byLabel.get(meta.label) ?? { order: meta.order, items: [] }
      g.items.push(c)
      byLabel.set(meta.label, g)
    }
    return [...byLabel.entries()]
      .sort((a, b) => a[1].order - b[1].order)
      .map(([label, g]) => ({ label, items: g.items.sort((a, b) => a.displayName.localeCompare(b.displayName)) }))
  }, [catalog, q])

  const addAtCenter = (kind: string) => {
    const el = document.querySelector('.react-flow')?.getBoundingClientRect()
    const at = el ? rf.screenToFlowPosition({ x: el.left + el.width / 2, y: el.top + el.height / 2 }) : { x: 0, y: 0 }
    useDesign.getState().addNode(kind, at)
  }

  return (
    <aside className="flex min-h-0 flex-col border-r border-line bg-panel">
      <div className="p-3">
        <input
          className="h-7 w-full rounded-md border border-line bg-panel-2 px-2 text-[12px] outline-none placeholder:text-faint focus:border-accent"
          placeholder="Search components"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
        {groups.map((g) => (
          <div key={g.label} className="mb-3">
            <div className="px-1.5 pb-1 text-[10.5px] font-semibold tracking-wide text-faint uppercase">{g.label}</div>
            {g.items.map((c) => (
              <button
                key={c.id}
                type="button"
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData(DRAG_MIME, c.id)
                  e.dataTransfer.effectAllowed = 'copy'
                }}
                onClick={() => addAtCenter(c.id)}
                title={`${c.id} — drag onto the canvas (or click to add)`}
                className="flex w-full cursor-grab items-center gap-2 rounded-md px-1.5 py-1 text-left hover:bg-panel-2 active:cursor-grabbing"
              >
                <span className="h-2.5 w-2.5 shrink-0 rounded-sm" style={{ background: archetypeMeta[c.archetype].color }} />
                <span className="truncate text-[12px]">{c.displayName}</span>
              </button>
            ))}
          </div>
        ))}
        {groups.length === 0 && <p className="px-2 text-[12px] text-muted">No component matches “{q}”.</p>}
      </div>
      <p className="border-t border-line px-3 py-2 text-[10.5px] leading-snug text-faint">
        Services go inside a Deployment or VM group; Deployments inside a Node Pool. Drag from a node's right handle to connect.
      </p>
    </aside>
  )
}
