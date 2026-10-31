import type { Archetype } from '../engine/types'

// Display metadata per archetype: palette grouping order and accent color
// (mid-tone hues that read on both light and dark backgrounds).
export const archetypeMeta: Record<Archetype, { label: string; color: string; order: number }> = {
  client: { label: 'Clients', color: '#7c8799', order: 0 },
  edge_proxy: { label: 'Load balancers', color: '#0ea5b7', order: 1 },
  gateway: { label: 'API gateways', color: '#8b5cf6', order: 2 },
  compute_service: { label: 'Services', color: '#3b7ae8', order: 3 },
  orchestrator: { label: 'Kubernetes', color: '#326ce5', order: 4 },
  node_pool: { label: 'Kubernetes', color: '#5b6b85', order: 4 },
  runtime_host: { label: 'VM groups', color: '#5b6b85', order: 5 },
  datastore: { label: 'Databases', color: '#0f9d8a', order: 6 },
  cache: { label: 'Caches', color: '#e0533d', order: 7 },
  queue_stream: { label: 'Queues & streams', color: '#d98a0b', order: 8 },
  external: { label: 'External', color: '#8a7f72', order: 9 },
}
