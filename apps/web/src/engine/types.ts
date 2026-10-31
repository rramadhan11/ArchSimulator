// TypeScript mirror of the engine's JSON (engine/model, engine/catalog,
// engine/sim). Keep in sync with the Go structs' json tags.

export type Config = Record<string, unknown>

export interface ModelNode {
  id: string
  kind: string
  name?: string
  parent?: string
  config?: Config
}

export interface Retry {
  max: number
  backoffMs: number
}

export interface ModelEdge {
  id?: string
  from: string
  to: string
  protocol?: string
  networkClass?: string
  latencyMs?: number
  op?: 'read' | 'write' | 'complex'
  classes?: string[]
  callsPerRequest?: number
  probability?: number
  when?: string
  parallelGroup?: string
  async?: boolean
  payloadReqKB?: number
  payloadRespKB?: number
  poolSize?: number
  keepAlive?: boolean
  timeoutMs?: number
  retry?: Retry
}

export interface RequestClass {
  name: string
  weight: number
}

export interface LoadPattern {
  kind: 'constant' | 'spike'
  peakTps?: number
  startS?: number
  rampS?: number
  holdS?: number
}

export interface TimelineConfig {
  durationS?: number
  load?: LoadPattern
  queueTimeoutMs?: number
  samplesPerTick?: number
}

export interface Workload {
  entry: string
  targetTps: number
  classes?: RequestClass[]
  slo?: { p99Ms?: number; errorRate?: number }
  thresholds?: { warn: number; overload: number }
  seed?: number
  samples?: number
  timeline?: TimelineConfig
}

/** Editor state stored in Architecture.ui; ignored by the engines. */
export interface EditorUI {
  positions?: Record<string, { x: number; y: number }>
  sizes?: Record<string, { width: number; height: number }>
}

export interface Architecture {
  version: string
  catalogVersion?: string
  nodes: ModelNode[]
  edges: ModelEdge[]
  workload: Workload
  ui?: EditorUI
}

// ---- catalog ----

export type Archetype =
  | 'client'
  | 'edge_proxy'
  | 'gateway'
  | 'compute_service'
  | 'runtime_host'
  | 'orchestrator'
  | 'node_pool'
  | 'datastore'
  | 'cache'
  | 'queue_stream'
  | 'external'

export type SourceKind = 'user' | 'vendor-docs' | 'vendor-benchmark' | 'default-estimate'

export interface Source {
  kind: SourceKind
  url?: string
  note?: string
}

export interface Param {
  default: unknown
  unit?: string
  description?: string
  source: Source
  options?: string[]
  optionsFrom?: 'instances' | 'profiles' | 'protocols'
}

export interface Component {
  id: string
  archetype: Archetype
  displayName: string
  version?: string
  description?: string
  params: Record<string, Param>
}

export interface Catalog {
  version: string
  components: Record<string, Component>
  instances: Record<string, { vcpu: number; memoryGiB: number }>
  instancesSource: Source
  networkClasses: Record<string, { rttMs: number; bandwidthMbps: number; source: Source }>
  protocols: Record<string, { serializeMsPerKB: number; multiplexed: boolean; defaultPoolSize: number; source: Source }>
  profiles: Record<string, { cpuMsPerReq: number; ioWaitMs: number; source: Source }>
}

// ---- quick check ----

export type Verdict = 'safe' | 'warning' | 'overload'
export type Severity = 'info' | 'warning' | 'error'

export interface Issue {
  severity: Severity
  ref: string
  message: string
}

export interface LatencyStats {
  meanMs: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
}

export interface ResourceReport {
  id: string
  node: string
  kind: string
  utilization: number | null
  load: number
  capacity: number
  unit: string
  scalingHeadroom?: boolean
  note?: string
}

export interface HopReport {
  node: string
  visitsPerRequest: number
  networkMs: number
  waitMs: number
  serviceMs: number
  delayMs: number
}

export interface ReplicaReport {
  service: string
  host?: string
  autoscaler?: string
  min?: number
  max?: number
  desired: number
  scheduled: number
  pending?: number
  consuming?: number
}

export interface PoolReport {
  node: string
  nodesNeeded: number
  nodesAvailable: number
  pendingPods?: number
  allocatableCpuPerNode: number
  allocatableMemGiBPerNode: number
}

export interface Assumption {
  node: string
  param: string
  value: unknown
  source: Source
}

export interface Report {
  tps: number
  verdict: Verdict
  reasons: string[] | null
  stable: boolean
  latency?: LatencyStats
  errorRate?: number
  resources: ResourceReport[] | null
  hops: HopReport[] | null
  async?: { edge: string; p50Ms: number; p99Ms: number }[]
  replicas?: ReplicaReport[]
  nodePools?: PoolReport[]
  issues?: Issue[]
  confidence: { score: number; level: 'low' | 'medium' | 'high'; criticalDefaults?: string[]; summary: string }
  assumptions: Assumption[]
}

export interface Result {
  catalogVersion: string
  target: Report
  maxSustainableTps: number | null
  limitedBy: string
  saturationOrder: ResourceReport[] | null
}

// ---- time simulation ----

export interface Tick {
  t: number
  offeredTps: number
  errorRate: number
  latency?: LatencyStats
  replicas: { desired: number; ready: number }[]
  stations: { load: number | null; backlog: number; waitMs: number }[]
  pools?: { ready: number; provisioning: number }[]
}

export interface Timeline {
  catalogVersion: string
  tickS: number
  services: string[]
  stations: string[]
  pools?: string[]
  ticks: Tick[]
  events: { t: number; ref: string; message: string }[] | null
  summary: { peakP99Ms: number | null; maxErrorRate: number; sloBreachS: number; firstBreachS: number | null }
  issues?: Issue[]
  assumptions: Assumption[]
}
