import type { Archetype, Failure, FailureKind } from '../engine/types'
import { useDesign } from '../store/design'
import { archetypeOf } from '../store/model'
import { Field, NumberInput, Section, Select } from '../ui/kit'

const CALLABLE: Archetype[] = ['edge_proxy', 'gateway', 'compute_service', 'datastore', 'cache', 'queue_stream', 'external']

const KINDS: Record<FailureKind, { label: string; hint: string; targets: Archetype[] | null; edges?: boolean; duration: string }> = {
  'instance-loss': {
    label: 'instance loss',
    hint: 'Nodes, VMs or pods die. Node groups, ASGs and Deployments replace them after their usual delay.',
    targets: ['node_pool', 'orchestrator', 'runtime_host', 'compute_service'],
    duration: 'unmanaged instances are back after (empty = never)',
  },
  'zone-outage': {
    label: 'zone outage',
    hint: 'One zone fails: every node pool, VM group and service loses its share of instances. Datastores, caches and load balancers are assumed multi-zone: use failover/outage for them.',
    targets: null,
    duration: 'unmanaged instances are back after (empty = never)',
  },
  outage: {
    label: 'dependency down',
    hint: 'Every call to the target fails: fast (error) or never answered (hang, callers wait for their timeout).',
    targets: CALLABLE,
    duration: 'empty = until the end',
  },
  latency: {
    label: 'latency injection',
    hint: 'Adds extraMs to every call of a component or edge, without using its CPU.',
    targets: CALLABLE,
    edges: true,
    duration: 'empty = until the end',
  },
  failover: {
    label: 'database failover',
    hint: 'The primary is unavailable; read replicas keep serving reads.',
    targets: ['datastore'],
    duration: "empty = the database's failoverS",
  },
}

export function FailuresEditor({ failures, onChange }: { failures: Failure[] | undefined; onChange: (f: Failure[]) => void }) {
  const list = failures ?? []
  const patch = (i: number, f: Partial<Failure>) =>
    onChange(
      list.map((x, k) => {
        if (k !== i) return x
        const next = { ...x, ...f }
        for (const key of Object.keys(next) as (keyof Failure)[]) if (next[key] === undefined) delete next[key]
        return next
      }),
    )
  return (
    <Section title="Failure injection">
      {list.length === 0 && <p className="text-[11px] text-muted">No failures: the run shows normal operation. Add one to see how the design degrades and recovers.</p>}
      {list.map((f, i) => (
        <FailureRow key={i} f={f} onPatch={(p) => patch(i, p)} onRemove={() => onChange(list.filter((_, k) => k !== i))} />
      ))}
      <button type="button" className="text-[11px] text-accent" onClick={() => onChange([...list, { kind: 'outage', atS: 60, durationS: 60 }])}>
        + add failure
      </button>
    </Section>
  )
}

function FailureRow({ f, onPatch, onRemove }: { f: Failure; onPatch: (p: Partial<Failure>) => void; onRemove: () => void }) {
  const nodes = useDesign((s) => s.nodes)
  const edges = useDesign((s) => s.edges)
  const catalog = useDesign((s) => s.catalog)
  const spec = KINDS[f.kind]
  const targets = [
    ...nodes
      .filter((n) => {
        const a = archetypeOf(catalog, n.data.kind)
        return a !== undefined && !!spec.targets?.includes(a)
      })
      .map((n) => ({ value: n.id, label: n.data.name || n.id })),
    ...(spec.edges ? edges.map((e) => ({ value: e.id, label: `edge ${e.id}` })) : []),
  ]
  const setKind = (kind: FailureKind) =>
    onPatch({
      kind,
      target: undefined,
      count: undefined,
      zones: undefined,
      mode: undefined,
      extraMs: kind === 'latency' ? 500 : undefined,
    })
  return (
    <div className="space-y-2 rounded-md border border-line p-2">
      <div className="flex items-center gap-2">
        <Select value={f.kind} options={(Object.keys(KINDS) as FailureKind[]).map((k) => ({ value: k, label: KINDS[k].label }))} onChange={(v) => setKind(v as FailureKind)} />
        <button type="button" className="px-1 text-muted hover:text-bad" title="Remove failure" onClick={onRemove}>
          ×
        </button>
      </div>
      <p className="text-[10.5px] text-muted">{spec.hint}</p>
      {spec.targets && (
        <Field label="target">
          {(id) => <Select id={id} value={f.target ?? ''} options={[{ value: '', label: '— select —' }, ...targets]} onChange={(v) => onPatch({ target: v || undefined })} />}
        </Field>
      )}
      <div className="grid grid-cols-2 gap-2">
        <Field label="atS (s)">{(id) => <NumberInput id={id} value={f.atS} min={0} onCommit={(v) => onPatch({ atS: v ?? 0 })} />}</Field>
        <Field label="durationS (s)" hint={spec.duration}>
          {(id) => <NumberInput id={id} value={f.durationS} min={0} placeholder="—" onCommit={(v) => onPatch({ durationS: v || undefined })} />}
        </Field>
        {f.kind === 'instance-loss' && <Field label="count">{(id) => <NumberInput id={id} value={f.count} min={1} placeholder="1" onCommit={(v) => onPatch({ count: v === undefined ? undefined : Math.round(v) })} />}</Field>}
        {f.kind === 'zone-outage' && <Field label="zones">{(id) => <NumberInput id={id} value={f.zones} min={1} placeholder="3" onCommit={(v) => onPatch({ zones: v === undefined ? undefined : Math.round(v) })} />}</Field>}
        {f.kind === 'latency' && <Field label="extraMs (ms)">{(id) => <NumberInput id={id} value={f.extraMs} min={0.001} onCommit={(v) => onPatch({ extraMs: v })} />}</Field>}
        {(f.kind === 'outage' || f.kind === 'failover') && (
          <Field label="mode">
            {(id) => (
              <Select
                id={id}
                value={f.mode ?? 'error'}
                options={[
                  { value: 'error', label: 'error (fail fast)' },
                  { value: 'hang', label: 'hang (until timeout)' },
                ]}
                onChange={(v) => onPatch({ mode: v === 'error' ? undefined : (v as 'hang') })}
              />
            )}
          </Field>
        )}
      </div>
    </div>
  )
}
