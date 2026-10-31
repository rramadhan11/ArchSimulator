import { Handle, NodeResizer, Position, type NodeProps } from '@xyflow/react'
import { memo, useMemo } from 'react'
import type { Report } from '../engine/types'
import { HEADER, NODE_H, NODE_W, PAD, type FlowNode } from '../store/model'
import { latest, useDesign } from '../store/design'
import { cx, heat } from '../ui/kit'
import { archetypeMeta } from './archetypes'

interface NodeInsight {
  util?: number | null // highest utilization among the node's resources
  error?: string // first error-severity issue on the node
  replicas?: string
  pool?: string
}

function insights(r: Report | undefined): Map<string, NodeInsight> {
  const m = new Map<string, NodeInsight>()
  if (!r) return m
  const get = (id: string) => m.get(id) ?? (m.set(id, {}), m.get(id)!)
  for (const res of r.resources ?? []) {
    const i = get(res.node)
    if (res.utilization === null) i.util = null
    else if (i.util !== null) i.util = Math.max(i.util ?? 0, res.utilization)
  }
  for (const is of r.issues ?? []) {
    if (is.severity === 'error') get(is.ref).error ??= is.message
  }
  for (const rep of r.replicas ?? []) {
    if (!rep.host) continue
    const scale = rep.autoscaler ? `${rep.autoscaler.toUpperCase()} ${rep.min}–${rep.max}: ` : ''
    get(rep.host).replicas = `${scale}${rep.scheduled}${rep.pending ? ` (+${rep.pending} pending)` : ''} replica${rep.scheduled === 1 ? '' : 's'}`
  }
  for (const p of r.nodePools ?? []) {
    get(p.node).pool = `${p.nodesNeeded}/${p.nodesAvailable} nodes`
  }
  return m
}

/** Per-node results of the latest quick check (re-computed when it changes). */
function useInsight(id: string): NodeInsight | undefined {
  const check = useDesign((s) => s.check)
  const map = useMemo(() => insights(latest(check)?.target), [check])
  return map.get(id)
}

function useLimits() {
  const t = useDesign((s) => s.workload.thresholds)
  return { warn: t?.warn ?? 0.7, overload: t?.overload ?? 0.85 }
}

const heatRing = { ok: 'ring-ok/70', warn: 'ring-warn', bad: 'ring-bad' }
const heatText = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad' }

export const ComponentNode = memo(function ComponentNode({ id, data, selected }: NodeProps<FlowNode>) {
  const comp = useDesign((s) => s.catalog.components[data.kind])
  const ins = useInsight(id)
  const { warn, overload } = useLimits()
  const meta = comp ? archetypeMeta[comp.archetype] : undefined
  const h = ins?.util !== undefined ? heat(ins.util, warn, overload) : undefined
  return (
    <div
      className={cx(
        'flex overflow-hidden rounded-lg border bg-panel shadow-[0_1px_2px_rgba(0,0,0,0.06)]',
        selected ? 'border-accent ring-2 ring-accent/30' : 'border-line',
        !selected && h && h !== 'ok' && `ring-2 ${heatRing[h]}`,
      )}
      style={{ width: NODE_W, height: NODE_H }}
    >
      <div className="w-1 shrink-0" style={{ background: meta?.color ?? 'var(--faint)' }} />
      <div className="flex min-w-0 flex-1 flex-col justify-center px-2.5">
        <div className="flex items-center gap-1.5">
          <span className="truncate text-[12.5px] font-semibold">{data.name || id}</span>
          {ins?.error && <span title={ins.error} className="h-1.5 w-1.5 shrink-0 rounded-full bg-bad" />}
          {h && (
            <span className={cx('num ml-auto shrink-0 text-[11px] font-semibold', heatText[h])}>
              {ins?.util === null ? 'n/a' : `${Math.round((ins?.util ?? 0) * 100)}%`}
            </span>
          )}
        </div>
        <div className="truncate text-[11px] text-muted">
          {comp?.displayName ?? <span className="text-bad">unknown kind {data.kind}</span>}
          {data.name && <span className="text-faint"> · {id}</span>}
        </div>
      </div>
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
    </div>
  )
})

export const ContainerNode = memo(function ContainerNode({ id, data, selected }: NodeProps<FlowNode>) {
  const comp = useDesign((s) => s.catalog.components[data.kind])
  const ins = useInsight(id)
  const meta = comp ? archetypeMeta[comp.archetype] : undefined
  const detail = ins?.replicas ?? ins?.pool
  return (
    <div
      className={cx('h-full w-full rounded-xl border-[1.5px] border-dashed', selected ? 'border-accent' : 'border-line-strong')}
      style={{ background: `color-mix(in srgb, ${meta?.color ?? '#888'} 5%, transparent)` }}
    >
      <NodeResizer isVisible={selected} minWidth={NODE_W + 2 * PAD} minHeight={HEADER + NODE_H + PAD} />
      <div className="flex items-center gap-1.5 px-3 text-[11px]" style={{ height: HEADER }} title={comp?.displayName}>
        <span className="h-2 w-2 shrink-0 rounded-sm" style={{ background: meta?.color }} />
        <span className="min-w-0 shrink truncate font-semibold">{data.name || id}</span>
        {ins?.error && <span title={ins.error} className="h-1.5 w-1.5 shrink-0 rounded-full bg-bad" />}
        <span className="num ml-auto min-w-0 truncate text-right text-[10.5px] text-muted">{detail ?? comp?.displayName}</span>
      </div>
    </div>
  )
})

export const nodeTypes = { component: ComponentNode, container: ContainerNode }
