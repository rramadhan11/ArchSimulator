import { Handle, NodeResizer, Position, type NodeProps } from '@xyflow/react'
import { memo, useMemo } from 'react'
import type { ReplicaTick, Report } from '../engine/types'
import { HEADER, NODE_H, NODE_W, PAD, type FlowNode } from '../store/model'
import { latest, useDesign } from '../store/design'
import { useNodeFrame, type NodeFrame } from '../store/playback'
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
function useCheckInsight(id: string): NodeInsight | undefined {
  const check = useDesign((s) => s.check)
  const map = useMemo(() => insights(latest(check)?.target), [check])
  return map.get(id)
}

/**
 * What the canvas shows for a node: the tick being played back, otherwise
 * the quick check. During playback, utilization is the offered load over
 * capacity at that second (above 100% the backlog grows).
 */
export function useInsight(id: string): (NodeInsight & { frame?: NodeFrame }) | undefined {
  const check = useCheckInsight(id)
  const frame = useNodeFrame(id)
  if (!frame) return check
  return { util: frame.load, frame }
}

export function useLimits() {
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
              {ins?.util === null ? (ins.frame ? 'down' : 'n/a') : `${Math.round((ins?.util ?? 0) * 100)}%`}
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

export const ContainerNode = memo(function ContainerNode({ id, data, selected, width }: NodeProps<FlowNode>) {
  const comp = useDesign((s) => s.catalog.components[data.kind])
  const ins = useInsight(id)
  const meta = comp ? archetypeMeta[comp.archetype] : undefined
  const detail = ins?.replicas ?? ins?.pool
  const frame = ins?.frame
  // Icons take at most half the header, so the name stays readable.
  const maxIcons = Math.max(3, Math.floor(((width ?? NODE_W + 2 * PAD) / 2 - 40) / ICON_STEP))
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
        {frame?.pods ? (
          <Pods pods={frame.pods} max={maxIcons} />
        ) : frame?.nodes ? (
          <PoolNodes {...frame.nodes} max={maxIcons} />
        ) : (
          <span className="num ml-auto min-w-0 truncate text-right text-[10.5px] text-muted">{frame ? comp?.displayName : (detail ?? comp?.displayName)}</span>
        )}
      </div>
    </div>
  )
})

const ICON_STEP = 10 // px per icon, gap included

/** A row of instance icons, at most max of them, then "+N". */
function Icons({ counts, max, title, label }: { counts: [number, string][]; max: number; title: string; label: string }) {
  const icons: string[] = []
  for (const [n, cls] of counts) for (let i = 0; i < n && icons.length < max; i++) icons.push(cls)
  const total = counts.reduce((a, [n]) => a + n, 0)
  return (
    <span className="ml-auto flex shrink-0 items-center gap-[2px]" title={title}>
      {icons.map((cls, i) => (
        <span key={i} className={cx('h-[8px] w-[8px] rounded-[2px]', cls)} />
      ))}
      {total > max && <span className="num ml-0.5 text-[10px] text-muted">+{total - max}</span>}
      <span className="num ml-1 text-[10.5px] text-muted">{label}</span>
    </span>
  )
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`

function Pods({ pods, max }: { pods: ReplicaTick; max: number }) {
  const parts = [`${pods.ready} ready`]
  if (pods.notReady) parts.push(`${pods.notReady} failing readiness`)
  if (pods.starting) parts.push(`${pods.starting} starting`)
  if (pods.pending) parts.push(`${pods.pending} Pending`)
  return (
    <Icons
      counts={[
        [pods.ready, 'bg-ok'],
        [pods.notReady, 'bg-warn'],
        [pods.starting, 'border-[1.5px] border-accent bg-accent/25'],
        [pods.pending, 'border-[1.5px] border-dashed border-faint'],
      ]}
      max={max}
      title={`${parts.join(', ')} (desired ${pods.desired})`}
      label={`${pods.ready}/${pods.desired}`}
    />
  )
}

function PoolNodes({ ready, provisioning, max }: { ready: number; provisioning: number; max: number }) {
  return (
    <Icons
      counts={[
        [ready, 'bg-muted'],
        [provisioning, 'border-[1.5px] border-muted'],
      ]}
      max={max}
      title={`${plural(ready, 'node')} ready${provisioning ? `, ${provisioning} provisioning` : ''}`}
      label={provisioning ? `${ready} + ${provisioning} nodes` : plural(ready, 'node')}
    />
  )
}

export const nodeTypes = { component: ComponentNode, container: ContainerNode }
