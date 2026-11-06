import { create } from 'zustand'
import type { ReplicaTick, Tick, Timeline } from '../engine/types'

/** Wall-clock seconds a whole playback takes; 0 plays in real time. */
export const PLAY_SPEEDS = [10, 30, 60, 0] as const

interface PlaybackState {
  /** The timeline on the canvas; null when the canvas shows the quick check. */
  tl: Timeline | null
  /** Index of the tick on the canvas. */
  tick: number
  playing: boolean
  speed: (typeof PLAY_SPEEDS)[number]

  play(tl: Timeline): void
  pause(): void
  /** Leaves playback: the canvas goes back to the quick check. */
  stop(): void
  seek(tl: Timeline, tick: number): void
  setSpeed(speed: PlaybackState['speed']): void
}

export const usePlayback = create<PlaybackState>()((set, get) => ({
  tl: null,
  tick: 0,
  playing: false,
  speed: 10,

  play(tl) {
    const atEnd = get().tl === tl && get().tick >= tl.ticks.length - 1
    set({ tl, tick: get().tl === tl && !atEnd ? get().tick : 0, playing: true })
    pos = get().tick
    last = performance.now()
    cancelAnimationFrame(frame)
    frame = requestAnimationFrame(advance)
  },

  pause() {
    cancelAnimationFrame(frame)
    set({ playing: false })
  },

  stop() {
    cancelAnimationFrame(frame)
    set({ tl: null, tick: 0, playing: false })
  },

  seek(tl, tick) {
    pos = tick
    set({ tl, tick })
  },

  setSpeed: (speed) => set({ speed }),
}))

// Fractional tick between frames; the store only changes when a whole tick passes.
let pos = 0
let last = 0
let frame = 0

function advance(now: number) {
  const { tl, speed, playing } = usePlayback.getState()
  if (!tl || !playing) return
  const n = tl.ticks.length
  const ticksPerS = speed === 0 ? 1 / tl.tickS : n / speed
  // A frame's timestamp can precede the performance.now() taken when play started.
  pos = Math.min(n - 1, pos + (Math.max(0, now - last) / 1000) * ticksPerS)
  last = now
  const tick = Math.floor(pos)
  if (tick !== usePlayback.getState().tick) usePlayback.setState({ tick })
  if (pos >= n - 1) usePlayback.setState({ playing: false })
  else frame = requestAnimationFrame(advance)
}

/** The tick on the canvas, or undefined outside playback. */
export function useFrame(): Tick | undefined {
  return usePlayback((s) => s.tl?.ticks[s.tick])
}

/** Where each design element's values sit in a timeline's per-tick arrays. */
interface FrameIndex {
  stations: Map<string, number[]> // node → its stations
  services: Map<string, number[]> // Deployment / VM group → the services on it
  pools: Map<string, number>
  calls: Map<string, number>
}

const indexes = new WeakMap<Timeline, FrameIndex>()

function frameIndex(tl: Timeline): FrameIndex {
  let ix = indexes.get(tl)
  if (ix) return ix
  const push = (m: Map<string, number[]>, k: string, i: number) => m.set(k, [...(m.get(k) ?? []), i])
  ix = { stations: new Map(), services: new Map(), pools: new Map(), calls: new Map() }
  tl.stations.forEach((id, i) => push(ix!.stations, id.slice(0, id.indexOf('/')), i)) // station ids are "<node>/<resource>"
  tl.hosts.forEach((host, i) => host && push(ix!.services, host, i))
  tl.pools?.forEach((id, i) => ix!.pools.set(id, i))
  tl.calls.forEach((id, i) => ix!.calls.set(id, i))
  indexes.set(tl, ix)
  return ix
}

export interface NodeFrame {
  /** Highest offered load over capacity among the node's resources; null when one is down. */
  load?: number | null
  /** Pods (or VMs) on a Deployment / VM group, summed over its services. */
  pods?: ReplicaTick
  nodes?: { ready: number; provisioning: number }
}

/** A node's state at the tick on the canvas, or undefined outside playback. */
export function useNodeFrame(id: string): NodeFrame | undefined {
  const tl = usePlayback((s) => s.tl)
  const tick = useFrame()
  if (!tl || !tick) return undefined
  const ix = frameIndex(tl)
  const f: NodeFrame = {}
  for (const i of ix.stations.get(id) ?? []) {
    const l = tick.stations[i].load
    f.load = l === null || f.load === null ? null : Math.max(f.load ?? 0, l)
  }
  const svcs = ix.services.get(id)
  if (svcs) {
    const p: ReplicaTick = { desired: 0, ready: 0, notReady: 0, starting: 0, pending: 0 }
    for (const i of svcs) for (const k of Object.keys(p) as (keyof ReplicaTick)[]) p[k] += tick.replicas[i][k]
    f.pods = p
  }
  const pool = ix.pools.get(id)
  if (pool !== undefined && tick.pools) f.nodes = tick.pools[pool]
  return f
}

/** Calls per second on an edge at the tick on the canvas, or undefined outside playback. */
export function useEdgeFrame(id: string): number | undefined {
  const tl = usePlayback((s) => s.tl)
  const tick = useFrame()
  if (!tl || !tick) return undefined
  const i = frameIndex(tl).calls.get(id)
  return i === undefined ? undefined : tick.calls[i]
}
