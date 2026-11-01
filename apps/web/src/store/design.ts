import { applyEdgeChanges, applyNodeChanges, type EdgeChange, type NodeChange, type XYPosition } from '@xyflow/react'
import { create } from 'zustand'
import { EngineError, engine } from '../engine/client'
import type { Architecture, Catalog, Config, Result, Timeline, Workload } from '../engine/types'
import {
  archetypeOf,
  canContain,
  edgeId,
  engineView,
  fromArchitecture,
  HEADER,
  isContainer,
  NODE_H,
  NODE_W,
  nodeId,
  PAD,
  renameNode,
  retargetFailures,
  sortParentsFirst,
  toArchitecture,
  type EdgeData,
  type FlowEdge,
  type FlowNode,
  type NodeData,
} from './model'
import { saveBaseline, saveLocal, type SavedDesign } from './persist'

export type Selection = { type: 'node'; id: string } | { type: 'edge'; id: string } | null

export type Status<T> =
  | { state: 'idle' }
  | { state: 'running'; last?: T }
  | { state: 'done'; value: T; ms: number } // ms: engine wall time, shown so slow devices are visible
  | { state: 'error'; message: string; last?: T }

/**
 * A pinned version of the design to compare against. Its results are
 * computed from the pinned design itself, so edits never leak into them.
 */
export interface Baseline {
  saved: SavedDesign
  check: Status<Result>
  timeline: Status<Timeline>
}

/** The newest value of a status, kept while a re-run is in flight or failed. */
export function latest<T>(s: Status<T>): T | undefined {
  return s.state === 'done' ? s.value : s.state === 'idle' ? undefined : s.last
}

interface DesignState {
  catalog: Catalog
  name: string
  nodes: FlowNode[]
  edges: FlowEdge[]
  workload: Workload
  selection: Selection
  check: Status<Result>
  timeline: Status<Timeline>
  baseline: Baseline | null

  load(d: SavedDesign): void
  /** Pins d (default: the current design) as the comparison baseline. */
  pinBaseline(d?: SavedDesign): void
  clearBaseline(): void
  architecture(): Architecture
  setName(name: string): void
  onNodesChange(changes: NodeChange<FlowNode>[]): void
  onEdgesChange(changes: EdgeChange<FlowEdge>[]): void
  select(s: Selection): void
  addNode(kind: string, at: XYPosition, parentId?: string): string
  reparent(id: string, parentId: string | undefined, position: XYPosition): void
  connect(source: string, target: string): void
  updateNode(id: string, patch: Partial<NodeData>): void
  setNodeConfig(id: string, config: Config): void
  renameNode(from: string, to: string): string | undefined
  updateEdge(id: string, patch: Partial<EdgeData>): void
  updateWorkload(patch: Partial<Workload>): void
  remove(sel: NonNullable<Selection>): void
  runCheck(): Promise<void>
  runTimeline(): Promise<void>
}

const errorMessage = (e: unknown) => (e instanceof Error ? e.message : String(e))

export const useDesign = create<DesignState>()((set, get) => ({
  catalog: undefined as unknown as Catalog, // set by initDesign before first render
  name: 'Untitled design',
  nodes: [],
  edges: [],
  workload: { entry: '', targetTps: 100 },
  selection: null,
  check: { state: 'idle' },
  timeline: { state: 'idle' },
  baseline: null,

  load(d) {
    const { nodes, edges, workload } = fromArchitecture(d.architecture, get().catalog)
    set({ name: d.name, nodes, edges, workload, selection: null, check: { state: 'idle' }, timeline: { state: 'idle' } })
  },

  pinBaseline(d = savedDesign()) {
    const saved = structuredClone(d)
    saveBaseline(saved)
    set({ baseline: { saved, check: { state: 'running' }, timeline: { state: 'idle' } } })
    void runForBaseline(saved, 'check')
  },

  clearBaseline() {
    saveBaseline(null)
    set({ baseline: null })
  },

  architecture() {
    const s = get()
    return toArchitecture(s.nodes, s.edges, s.workload, s.catalog.version)
  },

  setName: (name) => set({ name }),

  onNodesChange(changes) {
    // Deletions go through remove() so children and edges are cleaned up.
    const removed = changes.filter((c) => c.type === 'remove').map((c) => c.id)
    const rest = changes.filter((c) => c.type !== 'remove')
    if (rest.length) set({ nodes: applyNodeChanges(rest, get().nodes) })
    for (const id of removed) get().remove({ type: 'node', id })
  },

  onEdgesChange(changes) {
    set({ edges: applyEdgeChanges(changes, get().edges) })
  },

  select: (selection) => set({ selection }),

  addNode(kind, at, parentId) {
    if (parentId) at = insideContainer(at)
    const { nodes, catalog } = get()
    const id = nodeId(kind, new Set(nodes.map((n) => n.id)))
    const container = isContainer(archetypeOf(catalog, kind))
    const node: FlowNode = {
      id,
      type: container ? 'container' : 'component',
      position: at,
      ...(parentId ? { parentId } : {}),
      ...(container ? { width: NODE_W + 2 * PAD, height: HEADER + NODE_H + PAD } : {}),
      data: { kind, config: {} },
    }
    const next = sortParentsFirst([...nodes, node])
    const patch: Partial<DesignState> = { nodes: next, selection: { type: 'node', id } }
    if (!get().workload.entry && archetypeOf(catalog, kind) === 'client') {
      patch.workload = { ...get().workload, entry: id }
    }
    set(patch)
    if (parentId) fitContainer(parentId)
    return id
  },

  reparent(id, parentId, position) {
    if (parentId) position = insideContainer(position)
    set({
      nodes: sortParentsFirst(
        get().nodes.map((n) => {
          if (n.id !== id) return n
          const { parentId: _p, ...rest } = n
          return parentId ? { ...rest, parentId, position } : { ...rest, position }
        }),
      ),
    })
    if (parentId) fitContainer(parentId)
  },

  connect(source, target) {
    const { edges } = get()
    const id = edgeId(source, target, new Set(edges.map((e) => e.id)))
    set({ edges: [...edges, { id, source, target, data: {} }], selection: { type: 'edge', id } })
  },

  updateNode(id, patch) {
    set({ nodes: get().nodes.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } } : n)) })
  },

  setNodeConfig(id, config) {
    get().updateNode(id, { config })
  },

  renameNode(from, to) {
    to = to.trim()
    if (to === from) return undefined
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(to)) return 'Use letters, digits, ".", "_" or "-", starting with a letter or digit.'
    if (get().nodes.some((n) => n.id === to)) return `"${to}" is already used.`
    const s = get()
    const next = renameNode(s.nodes, s.edges, s.workload, from, to)
    const sel = s.selection?.type === 'node' && s.selection.id === from ? { type: 'node' as const, id: to } : s.selection
    set({ ...next, selection: sel })
    return undefined
  },

  updateEdge(id, patch) {
    set({
      edges: get().edges.map((e) => {
        if (e.id !== id) return e
        const data: EdgeData = { ...e.data, ...patch }
        for (const k of Object.keys(data) as (keyof EdgeData)[]) if (data[k] === undefined) delete data[k]
        return { ...e, data }
      }),
    })
  },

  updateWorkload(patch) {
    const w: Workload = { ...get().workload, ...patch }
    for (const k of Object.keys(w) as (keyof Workload)[]) if (w[k] === undefined) delete w[k]
    set({ workload: w })
  },

  remove(sel) {
    const s = get()
    if (sel.type === 'edge') {
      set({
        edges: s.edges.filter((e) => e.id !== sel.id),
        workload: retargetFailures(s.workload, (t) => (t === sel.id ? undefined : t)),
        selection: null,
      })
      return
    }
    // A container takes its contents with it.
    const doomed = new Set([sel.id])
    for (let grew = true; grew; ) {
      grew = false
      for (const n of s.nodes) {
        if (n.parentId && doomed.has(n.parentId) && !doomed.has(n.id)) {
          doomed.add(n.id)
          grew = true
        }
      }
    }
    const gone = new Set([...doomed, ...s.edges.filter((e) => doomed.has(e.source) || doomed.has(e.target)).map((e) => e.id)])
    const workload = retargetFailures(s.workload, (t) => (gone.has(t) ? undefined : t))
    set({
      nodes: s.nodes.filter((n) => !doomed.has(n.id)),
      edges: s.edges.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)),
      workload: doomed.has(workload.entry) ? { ...workload, entry: '' } : workload,
      selection: null,
    })
  },

  async runCheck() {
    const last = latest(get().check)
    set({ check: { state: 'running', last } })
    const req = ++checkSeq
    try {
      const { value, ms } = await engine.run(engineView(get().architecture()))
      if (req === checkSeq) set({ check: { state: 'done', value, ms } })
    } catch (e) {
      if (req === checkSeq) set({ check: { state: 'error', message: errorMessage(e), last } })
      if (!(e instanceof EngineError)) console.error(e)
    }
  },

  async runTimeline() {
    const last = latest(get().timeline)
    set({ timeline: { state: 'running', last } })
    try {
      const { value, ms } = await engine.simulate(engineView(get().architecture()))
      set({ timeline: { state: 'done', value, ms } })
    } catch (e) {
      set({ timeline: { state: 'error', message: errorMessage(e), last } })
      if (!(e instanceof EngineError)) console.error(e)
    }
    const b = get().baseline
    if (b && b.timeline.state !== 'done') await runForBaseline(b.saved, 'timeline')
  },
}))

/** Runs the engine on a baseline and stores the result if it is still pinned. */
async function runForBaseline(saved: SavedDesign, what: 'check' | 'timeline') {
  const patch = (status: Status<Result> | Status<Timeline>) => {
    const b = useDesign.getState().baseline
    if (b?.saved === saved) useDesign.setState({ baseline: { ...b, [what]: status } })
  }
  patch({ state: 'running' })
  try {
    const a = engineView(saved.architecture)
    const { value, ms } = what === 'check' ? await engine.run(a) : await engine.simulate(a)
    patch({ state: 'done', value, ms } as Status<Result> | Status<Timeline>)
  } catch (e) {
    patch({ state: 'error', message: errorMessage(e) })
  }
}

let checkSeq = 0

/** Keeps a child below its container's header and off the left border. */
function insideContainer(p: XYPosition): XYPosition {
  return { x: Math.max(PAD, p.x), y: Math.max(HEADER, p.y) }
}

/** Grows a container (and its ancestors) so its children fit inside. */
function fitContainer(id: string) {
  const { nodes } = useDesign.getState()
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const updated = new Map<string, FlowNode>()
  for (let cur = byId.get(id); cur; cur = cur.parentId ? byId.get(cur.parentId) : undefined) {
    let w = cur.width ?? NODE_W + 2 * PAD
    let h = cur.height ?? HEADER + NODE_H + PAD
    for (const k of nodes) {
      if (k.parentId !== cur.id) continue
      const kid = updated.get(k.id) ?? k
      w = Math.max(w, kid.position.x + (kid.width ?? kid.measured?.width ?? NODE_W) + PAD)
      h = Math.max(h, kid.position.y + (kid.height ?? kid.measured?.height ?? NODE_H) + PAD)
    }
    if (w !== cur.width || h !== cur.height) updated.set(cur.id, { ...cur, width: w, height: h })
  }
  if (updated.size) useDesign.setState({ nodes: nodes.map((n) => updated.get(n.id) ?? n) })
}

/** Which container (if any) may hold a node of this kind dropped at a point. */
export function dropTarget(kind: string, candidates: FlowNode[], excludeId?: string): FlowNode | undefined {
  const { catalog, nodes } = useDesign.getState()
  const child = archetypeOf(catalog, kind)
  if (!child) return undefined
  const kids = (id: string) => nodes.filter((n) => n.parentId === id && n.id !== excludeId).length
  // Prefer the innermost container (candidates come in render order, parents first).
  return [...candidates].reverse().find((c) => {
    const parent = archetypeOf(catalog, c.data.kind)
    return c.id !== excludeId && parent !== undefined && canContain(parent, child, kids(c.id))
  })
}

/** Snapshot for autosave/export/share. */
export function savedDesign(): SavedDesign {
  const s = useDesign.getState()
  return { name: s.name, architecture: s.architecture() }
}

let saveTimer: ReturnType<typeof setTimeout> | undefined
let checkTimer: ReturnType<typeof setTimeout> | undefined
let lastEngineKey = ''

/**
 * Wires the side effects: autosave on every change, and a debounced quick
 * check whenever the engine-visible part of the design changes (dragging
 * nodes around does not re-run the engine).
 */
export function startAutoEffects() {
  lastEngineKey = JSON.stringify(engineView(useDesign.getState().architecture()))
  return useDesign.subscribe((s, prev) => {
    if (s.nodes === prev.nodes && s.edges === prev.edges && s.workload === prev.workload && s.name === prev.name) return
    clearTimeout(saveTimer)
    saveTimer = setTimeout(() => saveLocal(savedDesign()), 300)

    const key = JSON.stringify(engineView(s.architecture()))
    if (key === lastEngineKey) return
    lastEngineKey = key
    clearTimeout(checkTimer)
    checkTimer = setTimeout(() => void useDesign.getState().runCheck(), 400)
  })
}
