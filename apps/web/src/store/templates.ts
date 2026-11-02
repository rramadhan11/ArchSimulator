// Templates are the engine's example designs (engine/examples), so the CLI
// golden tests and the editor share one set of fixtures.
import type { Architecture } from '../engine/types'
import type { SavedDesign } from './persist'

const files = import.meta.glob<Architecture>('../../../../engine/examples/*.json', { eager: true, import: 'default' })

const info: Record<string, { title: string; description: string }> = {
  'rest-postgres': { title: 'REST API + Postgres', description: 'A Go API on an EC2 auto scaling group behind an ALB, reading and writing Postgres.' },
  'k8s-kong-microservices': {
    title: 'Microservices on K8s + Kong',
    description: 'Kong in front of Go and Java services on Kubernetes, with Redis, Postgres and an external API; read/write request classes.',
  },
  'event-driven-kafka': { title: 'Event-driven with Kafka', description: 'A Go producer publishes to Kafka; Python consumers write to Postgres. Shows consumer lag and partition limits.' },
  'k8s-spike': { title: 'K8s HPA under a traffic spike', description: '300 → 1500 TPS spike: HPA scale-up, Pending pods and cluster-autoscaler nodes over time.' },
  'retry-storm': {
    title: 'Retry storm after a slow dependency',
    description: 'A 30 s slowdown plus retries without a budget leaves the dependency overloaded for good. Add a retry budget or a circuit breaker to fix it.',
  },
}

export interface Template {
  id: string
  title: string
  description: string
  nodes: number
  design: () => SavedDesign
}

const blank: Template = {
  id: 'blank',
  title: 'Blank canvas',
  description: 'One client to start from.',
  nodes: 1,
  design: () => ({
    name: 'Untitled design',
    architecture: {
      version: '0.1',
      nodes: [{ id: 'users', kind: 'client.web' }],
      edges: [],
      workload: { entry: 'users', targetTps: 100, slo: { p99Ms: 300, errorRate: 0.001 } },
    },
  }),
}

export const templates: Template[] = [
  blank,
  ...Object.entries(files)
    .map(([path, a]) => {
      const id = path.split('/').pop()!.replace(/\.json$/, '')
      const { title, description } = info[id] ?? { title: id, description: '' }
      return { id, title, description, nodes: a.nodes.length, design: () => ({ name: title, architecture: structuredClone(a) }) }
    })
    .sort((x, y) => x.title.localeCompare(y.title)),
]
