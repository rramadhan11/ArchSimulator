// Templates are the engine's example designs (engine/examples), so the CLI
// golden tests and the editor share one set of fixtures.
import type { Architecture } from '../engine/types'
import type { SavedDesign } from './persist'

const files = import.meta.glob<Architecture>('../../../../engine/examples/*.json', { eager: true, import: 'default' })

const titles: Record<string, string> = {
  'rest-postgres': 'REST API + Postgres',
  'k8s-kong-microservices': 'Microservices on K8s + Kong',
  'event-driven-kafka': 'Event-driven with Kafka',
  'k8s-spike': 'K8s HPA under a traffic spike',
  'retry-storm': 'Retry storm after a slow dependency',
}

export interface Template {
  id: string
  title: string
  design: () => SavedDesign
}

const blank: Template = {
  id: 'blank',
  title: 'Blank canvas',
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
      const title = titles[id] ?? id
      return { id, title, design: () => ({ name: title, architecture: structuredClone(a) }) }
    })
    .sort((x, y) => x.title.localeCompare(y.title)),
]
