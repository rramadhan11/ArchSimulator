import { describe, expect, it } from 'vitest'
import kongExample from '../../../../engine/examples/k8s-kong-microservices.json'
import type { Architecture, Catalog } from '../engine/types'
import { canContain, edgeId, engineView, fromArchitecture, getPath, renameNode, retargetFailures, setPath, toArchitecture } from './model'
import { decodeShare, encodeShare, parseSaved, type SavedDesign } from './persist'

// Only archetypes matter to the conversions.
const archetypes: Record<string, string> = {
  'client.mobile': 'client',
  'gateway.kong': 'gateway',
  'k8s.nodepool': 'node_pool',
  'k8s.deployment': 'orchestrator',
  'service.go': 'compute_service',
  'service.java': 'compute_service',
  'cache.redis': 'cache',
  'db.postgres': 'datastore',
  'ext.api': 'external',
}
const catalog = {
  version: '2026.10',
  components: Object.fromEntries(Object.entries(archetypes).map(([id, archetype]) => [id, { id, archetype, displayName: id, params: {} }])),
} as unknown as Catalog

const example = kongExample as unknown as Architecture

describe('architecture <-> flow', () => {
  it('round-trips everything the engine reads', () => {
    const { nodes, edges, workload } = fromArchitecture(example, catalog)
    const back = engineView(toArchitecture(nodes, edges, workload, catalog.version))
    const sortById = <T extends { id: string }>(xs: T[]) => [...xs].sort((a, b) => a.id.localeCompare(b.id))
    expect(sortById(back.nodes)).toEqual(sortById(example.nodes))
    // Edges gain explicit ids following the engine's own convention.
    expect(back.edges.map(({ id: _id, ...e }) => e)).toEqual(example.edges)
    expect(back.edges.map((e) => e.id)).toContain('orders->pg#2')
    expect(back.workload).toEqual(example.workload)
  })

  it('places parents before children and children inside their container', () => {
    const { nodes } = fromArchitecture(example, catalog)
    const index = new Map(nodes.map((n, i) => [n.id, i]))
    for (const n of nodes) {
      if (!n.parentId) continue
      expect(index.get(n.parentId)!).toBeLessThan(index.get(n.id)!)
      const parent = nodes.find((p) => p.id === n.parentId)!
      expect(n.position.x).toBeGreaterThanOrEqual(0)
      expect(n.position.y + 64).toBeLessThanOrEqual(parent.height!)
    }
  })

  it('renames a node everywhere it is referenced', () => {
    const { nodes, edges, workload } = fromArchitecture(example, catalog)
    const r = renameNode(nodes, edges, workload, 'redis', 'cache')
    expect(r.nodes.some((n) => n.id === 'redis')).toBe(false)
    expect(r.edges.find((e) => e.data?.when)?.data?.when).toBe('cache_miss:cache')
    expect(r.edges.some((e) => e.id === 'orders->cache')).toBe(true)
    const r2 = renameNode(nodes, edges, workload, 'orders-deploy', 'orders-dep')
    expect(r2.nodes.find((n) => n.id === 'orders')!.parentId).toBe('orders-dep')
    expect(renameNode(nodes, edges, workload, 'users', 'clients').workload.entry).toBe('clients')
  })

  it('keeps failure targets pointing at renamed nodes and edges, and drops deleted ones', () => {
    const { nodes, edges, workload } = fromArchitecture(example, catalog)
    const w = {
      ...workload,
      timeline: {
        failures: [
          { kind: 'outage' as const, target: 'redis', atS: 1 },
          { kind: 'latency' as const, target: 'orders->redis', atS: 2, extraMs: 5 },
          { kind: 'zone-outage' as const, atS: 3 },
        ],
      },
    }
    const r = renameNode(nodes, edges, w, 'redis', 'cache')
    expect(r.workload.timeline?.failures?.map((f) => f.target)).toEqual(['cache', 'orders->cache', undefined])
    const dropped = retargetFailures(w, (t) => (t === 'redis' ? undefined : t))
    expect(dropped.timeline?.failures?.map((f) => f.kind)).toEqual(['latency', 'zone-outage'])
  })
})

describe('helpers', () => {
  it('builds unique edge ids like the engine', () => {
    expect(edgeId('a', 'b', new Set())).toBe('a->b')
    expect(edgeId('a', 'b', new Set(['a->b']))).toBe('a->b#2')
  })

  it('reads and writes dotted config paths', () => {
    let cfg = setPath({}, 'hpa.max', 20)
    expect(cfg).toEqual({ hpa: { max: 20 } })
    expect(getPath(cfg, 'hpa.max')).toBe(20)
    cfg = setPath(cfg, 'hpa.max', undefined)
    expect(cfg).toEqual({ hpa: {} }) // an empty block still enables the HPA
  })

  it('follows the engine placement rules', () => {
    expect(canContain('node_pool', 'orchestrator', 3)).toBe(true)
    expect(canContain('orchestrator', 'compute_service', 0)).toBe(true)
    expect(canContain('orchestrator', 'compute_service', 1)).toBe(false)
    expect(canContain('node_pool', 'compute_service', 0)).toBe(false)
  })
})

describe('share links', () => {
  it('round-trips a design through the URL hash', async () => {
    const hash = await encodeShare({ name: 'demo', architecture: example })
    expect(hash.startsWith('#d=')).toBe(true)
    expect(await decodeShare(hash)).toEqual({ name: 'demo', architecture: example })
    expect(await decodeShare('#other')).toBeUndefined()
  })

  it('rejects oversized and malformed designs', async () => {
    const huge = { name: 'x', architecture: { ...example, nodes: Array.from({ length: 60000 }, (_, i) => ({ id: `n${i}`, kind: 'service.go', name: 'x'.repeat(40) })) } }
    await expect(decodeShare(await encodeShare(huge as unknown as SavedDesign))).rejects.toThrow(/too large/)
    expect(() => parseSaved('{"nodes": 1}')).toThrow(/not an ArchSim design/)
    expect(() => parseSaved('{"nodes": [{"id": 1}], "edges": [], "workload": {}}')).toThrow(/string id/)
  })
})
