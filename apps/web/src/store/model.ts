// Pure conversions between the engine's Architecture document and the
// editor's React Flow graph. The flow graph is the editing source of truth;
// toArchitecture() produces what the engine (and export/share) consume.
import type { Edge, Node } from '@xyflow/react'
import type { Archetype, Architecture, Catalog, Config, ModelEdge, ModelNode, Workload } from '../engine/types'

export type NodeData = { kind: string; name?: string; config: Config }
export type EdgeData = Omit<ModelEdge, 'id' | 'from' | 'to'>
export type FlowNode = Node<NodeData, 'component' | 'container'>
export type FlowEdge = Edge<EdgeData>

export const SCHEMA_VERSION = '0.1'

// Geometry shared by the canvas and the auto layout.
export const NODE_W = 200
export const NODE_H = 64
export const PAD = 24
export const HEADER = 34

const CONTAINERS: ReadonlySet<Archetype> = new Set(['node_pool', 'orchestrator', 'runtime_host'])

/** Container archetypes are drawn as groups that hold other nodes. */
export function isContainer(a: Archetype | undefined): boolean {
  return a !== undefined && CONTAINERS.has(a)
}

/**
 * Placement rules, mirroring checkPlacement in engine/sim/compile.go: a
 * service goes in a Deployment or VM group (one each), a Deployment in a
 * node pool. The engine re-validates; this only drives drag & drop.
 */
export function canContain(parent: Archetype, child: Archetype, parentChildren: number): boolean {
  switch (parent) {
    case 'node_pool':
      return child === 'orchestrator'
    case 'orchestrator':
    case 'runtime_host':
      return child === 'compute_service' && parentChildren === 0
    default:
      return false
  }
}

/** Mirrors callable() in the engine: hosts and pools cannot be called. */
export function isCallable(a: Archetype | undefined): boolean {
  return a !== undefined && !isContainer(a)
}

export function archetypeOf(cat: Catalog, kind: string): Archetype | undefined {
  return cat.components[kind]?.archetype
}

/** Default edge id, same convention as the engine (from->to, #index on clash). */
export function edgeId(from: string, to: string, taken: ReadonlySet<string>): string {
  const base = `${from}->${to}`
  if (!taken.has(base)) return base
  for (let i = 2; ; i++) {
    if (!taken.has(`${base}#${i}`)) return `${base}#${i}`
  }
}

/** A fresh node id derived from the kind: service.go -> go, go-2, ... */
export function nodeId(kind: string, taken: ReadonlySet<string>): string {
  const base = kind.split('.').pop() || 'node'
  if (!taken.has(base)) return base
  for (let i = 2; ; i++) {
    if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
  }
}

/** Orders nodes so every parent precedes its children (React Flow requires it). */
export function sortParentsFirst(nodes: FlowNode[]): FlowNode[] {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const depth = (n: FlowNode): number => {
    let d = 0
    for (let p = n.parentId; p && d < nodes.length; p = byId.get(p)?.parentId) d++
    return d
  }
  return nodes
    .map((n, i) => ({ n, i, d: depth(n) }))
    .sort((a, b) => a.d - b.d || a.i - b.i)
    .map((x) => x.n)
}

export function fromArchitecture(a: Architecture, cat: Catalog): { nodes: FlowNode[]; edges: FlowEdge[]; workload: Workload } {
  const ui = a.ui ?? {}
  let nodes: FlowNode[] = a.nodes.map((m) => {
    const container = isContainer(archetypeOf(cat, m.kind))
    const size = ui.sizes?.[m.id]
    return {
      id: m.id,
      type: container ? 'container' : 'component',
      position: ui.positions?.[m.id] ?? { x: 0, y: 0 },
      ...(m.parent ? { parentId: m.parent } : {}),
      ...(container ? { width: size?.width ?? NODE_W + 2 * PAD, height: size?.height ?? NODE_H + HEADER + PAD } : {}),
      data: { kind: m.kind, ...(m.name ? { name: m.name } : {}), config: m.config ?? {} },
    }
  })
  nodes = sortParentsFirst(nodes)
  if (!ui.positions) nodes = autoLayout(nodes, a.edges, a.workload.entry)

  const taken = new Set<string>()
  const edges: FlowEdge[] = a.edges.map((m) => {
    const { id, from, to, ...data } = m
    const eid = id || edgeId(from, to, taken)
    taken.add(eid)
    return { id: eid, source: from, target: to, data }
  })
  return { nodes, edges, workload: a.workload }
}

export function toArchitecture(nodes: FlowNode[], edges: FlowEdge[], workload: Workload, catalogVersion: string): Architecture {
  const modelNodes: ModelNode[] = nodes.map((n) => ({
    id: n.id,
    kind: n.data.kind,
    ...(n.data.name ? { name: n.data.name } : {}),
    ...(n.parentId ? { parent: n.parentId } : {}),
    ...(Object.keys(n.data.config).length ? { config: n.data.config } : {}),
  }))
  const modelEdges: ModelEdge[] = edges.map((e) => ({ id: e.id, from: e.source, to: e.target, ...e.data }))
  const positions: Record<string, { x: number; y: number }> = {}
  const sizes: Record<string, { width: number; height: number }> = {}
  for (const n of nodes) {
    positions[n.id] = { x: Math.round(n.position.x), y: Math.round(n.position.y) }
    if (n.type === 'container') {
      const width = n.width ?? n.measured?.width
      const height = n.height ?? n.measured?.height
      if (width && height) sizes[n.id] = { width: Math.round(width), height: Math.round(height) }
    }
  }
  return {
    version: SCHEMA_VERSION,
    catalogVersion,
    nodes: modelNodes,
    edges: modelEdges,
    workload,
    ui: { positions, sizes },
  }
}

/** The part of a design the engine reads; editor-only state is dropped. */
export function engineView(a: Architecture): Architecture {
  const { ui: _ui, ...rest } = a
  return rest
}

/**
 * Lays out a design that has no saved positions: top-level units (a node or
 * its outermost container) go in columns by call depth from the entry;
 * containers are sized to fit their children.
 */
export function autoLayout(nodes: FlowNode[], edges: Pick<ModelEdge, 'from' | 'to'>[], entry: string): FlowNode[] {
  const byId = new Map(nodes.map((n) => [n.id, { ...n }]))
  const children = new Map<string, FlowNode[]>()
  for (const n of byId.values()) {
    if (n.parentId) children.set(n.parentId, [...(children.get(n.parentId) ?? []), n])
  }

  // Size containers bottom-up and place children inside them.
  const size = (n: FlowNode): { w: number; h: number } => {
    if (n.type !== 'container') return { w: NODE_W, h: NODE_H }
    const kids = children.get(n.id) ?? []
    let y = HEADER
    let w = NODE_W
    for (const k of kids) {
      const s = size(k)
      k.position = { x: PAD, y }
      y += s.h + PAD
      w = Math.max(w, s.w)
    }
    const h = kids.length ? y : HEADER + NODE_H + PAD
    n.width = w + 2 * PAD
    n.height = h
    return { w: n.width, h }
  }

  // Longest call depth from the entry (the call graph is a DAG).
  const out = new Map<string, string[]>()
  for (const e of edges) out.set(e.from, [...(out.get(e.from) ?? []), e.to])
  const depth = new Map<string, number>()
  const visit = (id: string, d: number, path: Set<string>) => {
    if (path.has(id) || (depth.get(id) ?? -1) >= d) return
    depth.set(id, d)
    path.add(id)
    for (const to of out.get(id) ?? []) visit(to, d + 1, path)
    path.delete(id)
  }
  visit(entry, 0, new Set())

  const top = (n: FlowNode): FlowNode => {
    let cur = n
    while (cur.parentId && byId.has(cur.parentId)) cur = byId.get(cur.parentId)!
    return cur
  }
  const column = new Map<string, number>()
  for (const n of byId.values()) {
    const d = depth.get(n.id)
    if (d === undefined) continue
    const t = top(n)
    column.set(t.id, Math.min(column.get(t.id) ?? Infinity, d))
  }

  const tops = [...byId.values()].filter((n) => !n.parentId)
  const cols: FlowNode[][] = []
  const unreached = Math.max(-1, ...column.values()) + 1
  for (const n of tops) {
    const c = column.get(n.id) ?? unreached
    ;(cols[c] ??= []).push(n)
  }
  let x = 0
  for (const col of cols) {
    if (!col) continue
    const sizes = col.map(size)
    const total = sizes.reduce((s, z) => s + z.h, 0) + 40 * (col.length - 1)
    let y = -total / 2
    col.forEach((n, i) => {
      n.position = { x, y }
      y += sizes[i].h + 40
    })
    x += Math.max(...sizes.map((s) => s.w)) + 90
  }
  return nodes.map((n) => byId.get(n.id)!)
}

/** Renames a node and every reference to it (parents, edges, entry, when). */
export function renameNode(
  nodes: FlowNode[],
  edges: FlowEdge[],
  workload: Workload,
  from: string,
  to: string,
): { nodes: FlowNode[]; edges: FlowEdge[]; workload: Workload } {
  const swap = (id: string | undefined) => (id === from ? to : id)
  return {
    nodes: nodes.map((n) => ({ ...n, id: swap(n.id)!, ...(n.parentId ? { parentId: swap(n.parentId) } : {}) })),
    edges: edges.map((e) => {
      const data = { ...e.data }
      const m = data.when?.match(/^(cache_(?:hit|miss)):(.*)$/)
      if (m && m[2] === from) data.when = `${m[1]}:${to}`
      const source = swap(e.source)!
      const target = swap(e.target)!
      const id = e.id === `${e.source}->${e.target}` ? `${source}->${target}` : e.id
      return { ...e, id, source, target, data }
    }),
    workload: { ...workload, entry: swap(workload.entry)! },
  }
}

// ---- nested config access (catalog params use dotted paths) ----

export function getPath(cfg: Config, path: string): unknown {
  let cur: unknown = cfg
  for (const part of path.split('.')) {
    if (typeof cur !== 'object' || cur === null || Array.isArray(cur)) return undefined
    cur = (cur as Record<string, unknown>)[part]
  }
  return cur
}

/**
 * Returns a copy of cfg with path set; undefined removes the leaf. Emptied
 * parent objects are kept: their presence can matter (an empty "hpa" block
 * still enables the HPA with catalog defaults).
 */
export function setPath(cfg: Config, path: string, value: unknown): Config {
  const [head, ...rest] = path.split('.')
  const out: Config = { ...cfg }
  if (rest.length === 0) {
    if (value === undefined) delete out[head]
    else out[head] = value
    return out
  }
  const child = typeof out[head] === 'object' && out[head] !== null && !Array.isArray(out[head]) ? (out[head] as Config) : {}
  out[head] = setPath(child, rest.join('.'), value)
  return out
}
