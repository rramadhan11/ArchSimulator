import { useEffect, useState } from 'react'
import type { Archetype, Component, Config, ModelEdge, Param, RequestClass, Workload } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { archetypeOf, getPath, setPath, type FlowEdge, type FlowNode } from '../store/model'
import { Button, Chips, Field, fmtMs, NumberInput, Section, Select, SourceBadge, TextInput, Toggle, UtilBar } from '../ui/kit'
import { ParamField } from './ParamField'

export function Inspector() {
  const selection = useDesign((s) => s.selection)
  const node = useDesign((s) => (selection?.type === 'node' ? s.nodes.find((n) => n.id === selection.id) : undefined))
  const edge = useDesign((s) => (selection?.type === 'edge' ? s.edges.find((e) => e.id === selection.id) : undefined))
  return (
    <aside className="min-h-0 overflow-y-auto border-l border-line bg-panel">
      {node ? <NodeInspector node={node} /> : edge ? <EdgeInspector edge={edge} /> : <WorkloadInspector />}
    </aside>
  )
}

function Header({ title, subtitle, onDelete }: { title: string; subtitle: string; onDelete?: () => void }) {
  return (
    <div className="flex items-start gap-2 border-b border-line px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="truncate text-[14px] font-semibold">{title}</div>
        <div className="truncate text-[11.5px] text-muted">{subtitle}</div>
      </div>
      {onDelete && (
        <Button variant="danger" onClick={onDelete} title="Delete (Backspace)">
          Delete
        </Button>
      )}
    </div>
  )
}

// ---------------------------------------------------------------- node

/** Config blocks whose presence switches a feature on (engine: g.has(p, "hpa")). */
const toggleBlocks: Partial<Record<Archetype, { key: string; label: string; replaces: string }>> = {
  orchestrator: { key: 'hpa', label: 'Horizontal Pod Autoscaler', replaces: 'replicas' },
  runtime_host: { key: 'autoscaling', label: 'Autoscaling group', replaces: 'count' },
}

function visibleParams(comp: Component, node: FlowNode): [string, Param][] {
  const block = toggleBlocks[comp.archetype]
  const enabled = block ? getPath(node.data.config, block.key) !== undefined : false
  return Object.entries(comp.params).filter(([key]) => {
    if (comp.archetype === 'compute_service' && node.parentId && (key === 'replicas' || key === 'vcpu')) return false // the host decides
    if (block) {
      if (key.startsWith(block.key + '.')) return enabled
      if (key === block.replaces) return !enabled
    }
    return true
  })
}

function groupParams(params: [string, Param][]): { title: string; params: [string, Param][] }[] {
  const singles = params.filter(([k]) => !k.includes('.'))
  const groups = new Map<string, [string, Param][]>()
  for (const p of params) {
    if (!p[0].includes('.')) continue
    const g = p[0].split('.')[0]
    groups.set(g, [...(groups.get(g) ?? []), p])
  }
  return [{ title: 'Parameters', params: singles }, ...[...groups].map(([title, ps]) => ({ title, params: ps }))].filter((g) => g.params.length)
}

function NodeInspector({ node }: { node: FlowNode }) {
  const catalog = useDesign((s) => s.catalog)
  const { setNodeConfig, updateNode, remove } = useDesign.getState()
  const comp = catalog.components[node.data.kind]
  if (!comp) {
    return (
      <>
        <Header title={node.id} subtitle={`Unknown kind ${node.data.kind}`} onDelete={() => remove({ type: 'node', id: node.id })} />
        <p className="p-4 text-[12px] text-bad">This component is not in catalog {catalog.version}.</p>
      </>
    )
  }
  const cfg = node.data.config
  const onConfig = (c: Config) => setNodeConfig(node.id, c)
  const block = toggleBlocks[comp.archetype]

  return (
    <>
      <Header title={node.data.name || node.id} subtitle={`${comp.displayName}${comp.version ? ` ${comp.version}` : ''} · ${comp.archetype}`} onDelete={() => remove({ type: 'node', id: node.id })} />
      <Section title="Identity">
        <IdField id={node.id} />
        <Field label="name" hint="Display name (optional)">
          {(id) => <TextInput id={id} value={node.data.name} placeholder={node.id} onCommit={(v) => updateNode(node.id, { name: v })} />}
        </Field>
        {comp.description && <p className="text-[11.5px] text-muted">{comp.description}</p>}
      </Section>

      {comp.archetype === 'compute_service' && <RequestCost node={node} />}

      {block && (
        <Section title="Scaling">
          <Toggle
            checked={getPath(cfg, block.key) !== undefined}
            onChange={(on) => onConfig(setPath(cfg, block.key, on ? {} : undefined))}
            label={block.label}
          />
        </Section>
      )}

      {groupParams(visibleParams(comp, node)).map((g) => (
        <Section key={g.title} title={g.title}>
          {g.params.map(([key, p]) => (
            <ParamField key={key} name={key} param={p} config={cfg} catalog={catalog} onChange={onConfig} />
          ))}
        </Section>
      ))}

      <NodeResults id={node.id} />
      <RawConfig config={cfg} onChange={onConfig} />
    </>
  )
}

function IdField({ id }: { id: string }) {
  const [error, setError] = useState<string>()
  useEffect(() => setError(undefined), [id])
  return (
    <Field label="id" hint={error ?? 'Referenced by edges, results and the workload entry; renaming updates them.'}>
      {(fid) => <TextInput id={fid} value={id} onCommit={(v) => setError(v ? useDesign.getState().renameNode(id, v) : 'The id cannot be empty.')} className={error ? 'border-bad' : ''} />}
    </Field>
  )
}

/** CPU and IO per request: from the profile unless set, optionally per request class. */
function RequestCost({ node }: { node: FlowNode }) {
  const catalog = useDesign((s) => s.catalog)
  const classes = useDesign((s) => s.workload.classes)
  const cfg = node.data.config
  const comp = catalog.components[node.data.kind]
  const profileName = (getPath(cfg, 'profile') as string | undefined) ?? (comp.params.profile?.default as string)
  const profile = catalog.profiles[profileName]
  const set = (path: string, v: number | undefined) => useDesign.getState().setNodeConfig(node.id, setPath(cfg, path, v))
  const fields = [
    { key: 'cpuMsPerReq', label: 'cpuMsPerReq (ms)', def: profile?.cpuMsPerReq, hint: 'CPU time per request on one core. The main capacity driver: measure it (profiling or load test).' },
    { key: 'ioWaitMs', label: 'ioWaitMs (ms)', def: profile?.ioWaitMs, hint: 'Waiting on unmodeled IO; holds a request slot but no CPU.' },
  ]
  return (
    <Section title="Request cost">
      {fields.map((f) => {
        const v = getPath(cfg, f.key) as number | undefined
        return (
          <Field
            key={f.key}
            label={f.label}
            hint={f.hint}
            right={<SourceBadge source={v !== undefined ? { kind: 'user' } : { ...(profile?.source ?? { kind: 'default-estimate' }), note: `profile ${profileName}` }} />}
          >
            {(id) => <NumberInput id={id} value={v} min={0} placeholder={f.def !== undefined ? String(f.def) : ''} onCommit={(n) => set(f.key, n)} />}
          </Field>
        )
      })}
      {classes && classes.length > 1 && (
        <div>
          <div className="mb-1 text-[11px] text-muted">cpuMsPerReq per request class (optional)</div>
          <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1">
            {classes.map((c) => (
              <div key={c.name} className="contents">
                <span className="font-mono text-[11px] text-muted">{c.name}</span>
                <NumberInput value={getPath(cfg, `classes.${c.name}.cpuMsPerReq`) as number | undefined} min={0} placeholder="same" onCommit={(n) => set(`classes.${c.name}.cpuMsPerReq`, n)} />
              </div>
            ))}
          </div>
        </div>
      )}
    </Section>
  )
}

function NodeResults({ id }: { id: string }) {
  const result = useDesign((s) => latest(s.check))
  const t = useDesign((s) => s.workload.thresholds)
  if (!result) return null
  const r = result.target
  const resources = (r.resources ?? []).filter((x) => x.node === id)
  const hop = r.hops?.find((h) => h.node === id)
  const issues = (r.issues ?? []).filter((i) => i.ref === id)
  if (!resources.length && !hop && !issues.length) return null
  return (
    <Section title={`At ${Math.round(r.tps)} TPS`}>
      {resources.map((x) => (
        <div key={x.id}>
          <div className="mb-0.5 flex justify-between gap-2 text-[11px]">
            <span className="truncate font-mono">{x.kind}</span>
            <span className="num shrink-0 text-muted">
              {Math.round(x.load).toLocaleString()} / {Math.round(x.capacity).toLocaleString()} {x.unit}
            </span>
          </div>
          <UtilBar u={x.utilization} warn={t?.warn ?? 0.7} overload={t?.overload ?? 0.85} />
          {x.note && <p className="mt-0.5 text-[10.5px] text-faint">{x.note}</p>}
        </div>
      ))}
      {hop && (
        <p className="text-[11.5px] text-muted">
          Per request: network {fmtMs(hop.networkMs)} · wait {fmtMs(hop.waitMs)} · service {fmtMs(hop.serviceMs)} · delay {fmtMs(hop.delayMs)} ({hop.visitsPerRequest.toFixed(2)} visits)
        </p>
      )}
      {issues.map((i, k) => (
        <p key={k} className={i.severity === 'error' ? 'text-[11.5px] text-bad' : i.severity === 'warning' ? 'text-[11.5px] text-warn' : 'text-[11.5px] text-muted'}>
          {i.message}
        </p>
      ))}
    </Section>
  )
}

function RawConfig({ config, onChange }: { config: Config; onChange: (c: Config) => void }) {
  const [text, setText] = useState(() => JSON.stringify(config, null, 2))
  const [error, setError] = useState<string>()
  useEffect(() => setText(JSON.stringify(config, null, 2)), [config])
  const apply = () => {
    try {
      const v: unknown = JSON.parse(text || '{}')
      if (typeof v !== 'object' || v === null || Array.isArray(v)) throw new Error('config must be a JSON object')
      setError(undefined)
      onChange(v as Config)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <Section title="Raw config (JSON)" defaultOpen={false}>
      <textarea
        className="h-40 w-full rounded-md border border-line bg-panel-2 p-2 font-mono text-[11px] outline-none focus:border-accent"
        value={text}
        spellCheck={false}
        onChange={(e) => setText(e.target.value)}
        onBlur={apply}
      />
      {error && <p className="text-[11px] text-bad">{error}</p>}
    </Section>
  )
}

// ---------------------------------------------------------------- edge

function EdgeInspector({ edge }: { edge: FlowEdge }) {
  const catalog = useDesign((s) => s.catalog)
  const nodes = useDesign((s) => s.nodes)
  const classes = useDesign((s) => s.workload.classes)
  const { updateEdge, remove } = useDesign.getState()
  const d = edge.data ?? {}
  const up = (patch: Partial<ModelEdge>) => updateEdge(edge.id, patch)
  const kindOf = (id: string) => nodes.find((n) => n.id === id)?.data.kind ?? ''
  const target = catalog.components[kindOf(edge.target)]
  const defaultProtocol = target?.params.protocol?.default as string | undefined
  const caches = nodes.filter((n) => archetypeOf(catalog, n.data.kind) === 'cache')
  const fromClient = archetypeOf(catalog, kindOf(edge.source)) === 'client'

  return (
    <>
      <Header title={`${edge.source} → ${edge.target}`} subtitle={`edge ${edge.id}`} onDelete={() => remove({ type: 'edge', id: edge.id })} />
      <Section title="Protocol & network">
        <Field label="protocol" hint={defaultProtocol ? `Default comes from ${target?.displayName}.` : 'Default: http1.1+json.'}>
          {(id) => (
            <Select
              id={id}
              value={d.protocol ?? ''}
              options={[{ value: '', label: `default (${defaultProtocol ?? 'http1.1+json'})` }, ...Object.keys(catalog.protocols).sort().map((p) => ({ value: p, label: p }))]}
              onChange={(v) => up({ protocol: v || undefined })}
            />
          )}
        </Field>
        <Field label="networkClass">
          {(id) => (
            <Select
              id={id}
              value={d.networkClass ?? ''}
              options={[
                { value: '', label: `default (${fromClient ? 'internet' : 'same-az'})` },
                ...Object.entries(catalog.networkClasses).map(([k, v]) => ({ value: k, label: `${k} · ${v.rttMs} ms RTT` })),
              ]}
              onChange={(v) => up({ networkClass: v || undefined })}
            />
          )}
        </Field>
        <Field label="latencyMs (ms)" hint="Overrides the network class round-trip time.">
          {(id) => <NumberInput id={id} value={d.latencyMs} min={0} placeholder="from class" onCommit={(v) => up({ latencyMs: v })} />}
        </Field>
        <div className="grid grid-cols-2 gap-2">
          <Field label="payloadReqKB">{(id) => <NumberInput id={id} value={d.payloadReqKB} min={0} placeholder="1" onCommit={(v) => up({ payloadReqKB: v })} />}</Field>
          <Field label="payloadRespKB">{(id) => <NumberInput id={id} value={d.payloadRespKB} min={0} placeholder="1" onCommit={(v) => up({ payloadRespKB: v })} />}</Field>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <Field label="poolSize" hint="Connections per caller replica.">
            {(id) => <NumberInput id={id} value={d.poolSize} min={0} placeholder="protocol default" onCommit={(v) => up({ poolSize: v === undefined ? undefined : Math.round(v) })} />}
          </Field>
          <Field label="keepAlive">
            {(id) => <Toggle id={id} checked={d.keepAlive ?? true} onChange={(v) => up({ keepAlive: v ? undefined : false })} label={<span className="text-muted">{d.keepAlive === false ? 'off (handshake per call)' : 'on'}</span>} />}
          </Field>
        </div>
      </Section>

      <Section title="Call pattern">
        {target?.archetype === 'datastore' && (
          <Field label="op" hint="Database operation; costs come from the database's opCostMs.">
            {(id) => (
              <Select
                id={id}
                value={d.op ?? ''}
                options={[{ value: '', label: 'default (read)' }, { value: 'read', label: 'read' }, { value: 'write', label: 'write' }, { value: 'complex', label: 'complex' }]}
                onChange={(v) => up({ op: (v || undefined) as ModelEdge['op'] })}
              />
            )}
          </Field>
        )}
        <Field label="callsPerRequest" hint="Average calls per visit of the caller (fan-out).">
          {(id) => <NumberInput id={id} value={d.callsPerRequest} min={0} placeholder="1" onCommit={(v) => up({ callsPerRequest: v })} />}
        </Field>
        <Field label="condition" hint="Call only on a cache miss/hit (uses the cache's hitRatio), or with a fixed probability.">
          {(id) => (
            <Select
              id={id}
              value={d.when ?? (d.probability !== undefined ? 'probability' : '')}
              options={[
                { value: '', label: 'always' },
                { value: 'probability', label: 'with probability…' },
                ...caches.flatMap((c) => [
                  { value: `cache_miss:${c.id}`, label: `on cache miss of ${c.id}` },
                  { value: `cache_hit:${c.id}`, label: `on cache hit of ${c.id}` },
                ]),
              ]}
              onChange={(v) => up(v === 'probability' ? { when: undefined, probability: d.probability ?? 0.5 } : { when: v || undefined, probability: undefined })}
            />
          )}
        </Field>
        {d.probability !== undefined && !d.when && (
          <Field label="probability">{(id) => <NumberInput id={id} value={d.probability} min={0} max={1} onCommit={(v) => up({ probability: v })} />}</Field>
        )}
        {classes && classes.length > 0 && (
          <Field label="classes" hint="Only these request classes take this edge (none selected = all).">
            {() => <Chips options={classes.map((c) => c.name)} value={d.classes ?? []} onChange={(v) => up({ classes: v.length ? v : undefined })} />}
          </Field>
        )}
        <Field label="parallelGroup" hint="Calls from the same caller with the same group run in parallel (latency = max).">
          {(id) => <TextInput id={id} value={d.parallelGroup} placeholder="sequential" onCommit={(v) => up({ parallelGroup: v })} />}
        </Field>
        <Field label="async" hint="Fire-and-forget: the caller does not wait. Edges out of a queue are always async.">
          {(id) => <Toggle id={id} checked={!!d.async} onChange={(v) => up({ async: v || undefined })} />}
        </Field>
      </Section>

      <Section title="Resilience">
        <Field label="timeoutMs (ms)" hint="A call slower than this fails the request.">
          {(id) => <NumberInput id={id} value={d.timeoutMs} min={0} placeholder="none" onCommit={(v) => up({ timeoutMs: v })} />}
        </Field>
        <div className="grid grid-cols-2 gap-2">
          <Field label="retry.max">
            {(id) => <NumberInput id={id} value={d.retry?.max} min={0} placeholder="0" onCommit={(v) => up({ retry: v ? { max: Math.round(v), backoffMs: d.retry?.backoffMs ?? 0 } : undefined })} />}
          </Field>
          <Field label="retry.backoffMs">
            {(id) => <NumberInput id={id} value={d.retry?.backoffMs} min={0} placeholder="0" onCommit={(v) => d.retry && up({ retry: { ...d.retry, backoffMs: v ?? 0 } })} />}
          </Field>
        </div>
        <p className="text-[11px] text-warn">Retries are saved with the design but not simulated yet (planned for Phase 2).</p>
      </Section>
    </>
  )
}

// ---------------------------------------------------------------- workload

function WorkloadInspector() {
  const w = useDesign((s) => s.workload)
  const nodes = useDesign((s) => s.nodes)
  const catalog = useDesign((s) => s.catalog)
  const name = useDesign((s) => s.name)
  const { updateWorkload, setName } = useDesign.getState()
  const clients = nodes.filter((n) => archetypeOf(catalog, n.data.kind) === 'client')
  const tl = w.timeline ?? {}
  const load = tl.load ?? { kind: 'constant' as const }
  const setTimeline = (patch: Partial<NonNullable<Workload['timeline']>>) => updateWorkload({ timeline: { ...tl, ...patch } })

  return (
    <>
      <Header title="Workload" subtitle="Select a node or edge to edit it" />
      <Section title="Design">
        <Field label="name">{(id) => <TextInput id={id} value={name} onCommit={(v) => setName(v ?? 'Untitled design')} />}</Field>
      </Section>
      <Section title="Traffic">
        <Field label="entry" hint="The client node requests start from.">
          {(id) => (
            <Select
              id={id}
              value={w.entry}
              options={[{ value: '', label: '— select a client —' }, ...clients.map((c) => ({ value: c.id, label: c.data.name || c.id }))]}
              onChange={(v) => updateWorkload({ entry: v })}
            />
          )}
        </Field>
        <Field label="targetTps" hint="Requests per second for the quick check (and the starting load of the time simulation).">
          {(id) => <NumberInput id={id} value={w.targetTps} min={0.001} onCommit={(v) => v !== undefined && updateWorkload({ targetTps: v })} />}
        </Field>
        <ClassesEditor classes={w.classes} onChange={(classes) => updateWorkload({ classes })} />
      </Section>
      <Section title="SLO & thresholds">
        <div className="grid grid-cols-2 gap-2">
          <Field label="p99 (ms)">{(id) => <NumberInput id={id} value={w.slo?.p99Ms} min={0} placeholder="none" onCommit={(v) => updateWorkload({ slo: { ...w.slo, p99Ms: v } })} />}</Field>
          <Field label="error rate">{(id) => <NumberInput id={id} value={w.slo?.errorRate} min={0} max={1} placeholder="0.001" onCommit={(v) => updateWorkload({ slo: { ...w.slo, errorRate: v } })} />}</Field>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <Field label="warn at">
            {(id) => <NumberInput id={id} value={w.thresholds?.warn} min={0} max={1} placeholder="0.70" onCommit={(v) => updateWorkload({ thresholds: v === undefined && !w.thresholds?.overload ? undefined : { warn: v ?? 0.7, overload: w.thresholds?.overload ?? 0.85 } })} />}
          </Field>
          <Field label="overload at">
            {(id) => <NumberInput id={id} value={w.thresholds?.overload} min={0} max={1} placeholder="0.85" onCommit={(v) => updateWorkload({ thresholds: v === undefined && !w.thresholds?.warn ? undefined : { warn: w.thresholds?.warn ?? 0.7, overload: v ?? 0.85 } })} />}
          </Field>
        </div>
      </Section>
      <Section title="Time simulation">
        <Field label="load pattern">
          {(id) => (
            <Select
              id={id}
              value={load.kind}
              options={[
                { value: 'constant', label: 'constant (targetTps)' },
                { value: 'spike', label: 'spike' },
              ]}
              onChange={(kind) => setTimeline({ load: kind === 'spike' ? { kind, peakTps: w.targetTps * 3, startS: 60, rampS: 10, holdS: 120 } : undefined })}
            />
          )}
        </Field>
        {load.kind === 'spike' && (
          <div className="grid grid-cols-2 gap-2">
            <Field label="peakTps">{(id) => <NumberInput id={id} value={load.peakTps} min={0.001} onCommit={(v) => setTimeline({ load: { ...load, peakTps: v } })} />}</Field>
            <Field label="startS (s)">{(id) => <NumberInput id={id} value={load.startS} min={0} placeholder="0" onCommit={(v) => setTimeline({ load: { ...load, startS: v } })} />}</Field>
            <Field label="rampS (s)">{(id) => <NumberInput id={id} value={load.rampS} min={0} placeholder="0" onCommit={(v) => setTimeline({ load: { ...load, rampS: v } })} />}</Field>
            <Field label="holdS (s)">{(id) => <NumberInput id={id} value={load.holdS} min={0} placeholder="0" onCommit={(v) => setTimeline({ load: { ...load, holdS: v } })} />}</Field>
          </div>
        )}
        <div className="grid grid-cols-2 gap-2">
          <Field label="durationS (s)">{(id) => <NumberInput id={id} value={tl.durationS} min={1} max={3600} placeholder="600" onCommit={(v) => setTimeline({ durationS: v === undefined ? undefined : Math.round(v) })} />}</Field>
          <Field label="queueTimeoutMs">{(id) => <NumberInput id={id} value={tl.queueTimeoutMs} min={0} placeholder="30000" onCommit={(v) => setTimeline({ queueTimeoutMs: v })} />}</Field>
        </div>
      </Section>
      <Section title="Sampling" defaultOpen={false}>
        <div className="grid grid-cols-2 gap-2">
          <Field label="samples" hint="Monte Carlo requests (quick check).">{(id) => <NumberInput id={id} value={w.samples} min={100} placeholder="20000" onCommit={(v) => updateWorkload({ samples: v === undefined ? undefined : Math.round(v) })} />}</Field>
          <Field label="seed">{(id) => <NumberInput id={id} value={w.seed} min={1} placeholder="1" onCommit={(v) => updateWorkload({ seed: v === undefined ? undefined : Math.round(v) })} />}</Field>
        </div>
      </Section>
    </>
  )
}

function ClassesEditor({ classes, onChange }: { classes: RequestClass[] | undefined; onChange: (c: RequestClass[] | undefined) => void }) {
  const list = classes ?? []
  const total = list.reduce((s, c) => s + c.weight, 0)
  const patch = (i: number, c: Partial<RequestClass>) => onChange(list.map((x, k) => (k === i ? { ...x, ...c } : x)))
  return (
    <div>
      <div className="mb-1 flex items-center">
        <span className="flex-1 font-mono text-[11.5px]">classes</span>
        <button type="button" className="text-[11px] text-accent" onClick={() => onChange([...list, { name: `class${list.length + 1}`, weight: 1 }])}>
          + add
        </button>
      </div>
      {list.length === 0 && <p className="text-[11px] text-muted">One request class. Add classes (e.g. read 80 / write 20) to route edges per class.</p>}
      <div className="space-y-1">
        {list.map((c, i) => (
          <div key={i} className="grid grid-cols-[1fr_70px_40px_auto] items-center gap-1.5">
            <TextInput value={c.name} onCommit={(v) => v && patch(i, { name: v })} />
            <NumberInput value={c.weight} min={0.0001} onCommit={(v) => v !== undefined && patch(i, { weight: v })} />
            <span className="num text-right text-[11px] text-muted">{total ? Math.round((c.weight / total) * 100) : 0}%</span>
            <button type="button" className="px-1 text-muted hover:text-bad" title="Remove class" onClick={() => onChange(list.length > 1 ? list.filter((_, k) => k !== i) : undefined)}>
              ×
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
