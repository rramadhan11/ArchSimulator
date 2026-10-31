import { useState } from 'react'
import type { Assumption, Issue, Result } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { cx, fmt, fmtMs, SourceBadge, UtilBar, verdictStyle } from '../ui/kit'

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'ok' | 'warn' | 'bad' }) {
  return (
    <div className="min-w-0 rounded-lg border border-line bg-panel px-3 py-2">
      <div className="text-[10.5px] font-medium tracking-wide text-muted uppercase">{label}</div>
      <div className={cx('num truncate text-[17px] font-semibold', tone === 'ok' && 'text-ok', tone === 'warn' && 'text-warn', tone === 'bad' && 'text-bad')}>{value}</div>
      {sub && <div className="truncate text-[11px] text-muted" title={sub}>{sub}</div>}
    </div>
  )
}

/** "resource:orders/cpu" -> "orders/cpu", "slo.p99" -> "p99 SLO", ... */
function limitLabel(by: string): string {
  if (by.startsWith('resource:')) return by.slice('resource:'.length)
  if (by.startsWith('issue:')) return `issue on ${by.slice('issue:'.length)}`
  return { 'slo.p99': 'p99 SLO', 'slo.errorRate': 'error budget', unstable: 'unbounded queues' }[by] ?? by
}

function selectRef(ref: string) {
  const s = useDesign.getState()
  if (s.nodes.some((n) => n.id === ref)) s.select({ type: 'node', id: ref })
  else if (s.edges.some((e) => e.id === ref)) s.select({ type: 'edge', id: ref })
}

function RefLink({ refId }: { refId: string }) {
  return (
    <button type="button" className="font-mono text-accent hover:underline" onClick={() => selectRef(refId)}>
      {refId}
    </button>
  )
}

const severityCls: Record<Issue['severity'], string> = { error: 'text-bad', warning: 'text-warn', info: 'text-muted' }

export function QuickCheck() {
  const check = useDesign((s) => s.check)
  const t = useDesign((s) => s.workload.thresholds)
  const slo = useDesign((s) => s.workload.slo)
  const result = latest(check)

  if (check.state === 'error' && !result) return <EngineErrorView message={check.message} />
  if (!result) return <p className="p-4 text-muted">{check.state === 'running' ? 'Running quick check…' : 'Add components and connect them to start.'}</p>

  const r = result.target
  const warn = t?.warn ?? 0.7
  const overload = t?.overload ?? 0.85
  const v = verdictStyle[r.verdict]
  const p99Tone = !r.latency ? 'bad' : slo?.p99Ms && r.latency.p99Ms > slo.p99Ms ? 'bad' : slo?.p99Ms && r.latency.p99Ms > 0.8 * slo.p99Ms ? 'warn' : undefined

  return (
    <div className={cx('space-y-4 p-4 transition-opacity', check.state === 'running' && 'opacity-60')}>
      {check.state === 'error' && <EngineErrorView message={check.message} stale />}
      <div className={cx('flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg px-3 py-2.5', v.cls)}>
        <span className="text-[15px] font-bold tracking-wide uppercase">{v.label}</span>
        <span className="num text-[13px]">at {fmt(r.tps)} TPS</span>
        {check.state === 'done' && <span className="num ml-auto text-[10.5px] text-muted">computed in {fmt(check.ms)} ms</span>}
        <ul className="w-full list-disc space-y-0.5 pl-5 text-[12px] text-fg/90 empty:hidden">
          {(r.reasons ?? []).map((x, i) => (
            <li key={i}>{x}</li>
          ))}
        </ul>
      </div>

      <div className="grid grid-cols-2 gap-2 lg:grid-cols-5">
        <Stat label="p50 / p95" value={r.latency ? `${fmtMs(r.latency.p50Ms)}` : 'unbounded'} sub={r.latency ? `p95 ${fmtMs(r.latency.p95Ms)} · mean ${fmtMs(r.latency.meanMs)}` : 'queues grow without bound'} />
        <Stat label="p99" value={r.latency ? fmtMs(r.latency.p99Ms) : 'unbounded'} sub={slo?.p99Ms ? `SLO ${fmtMs(slo.p99Ms)}` : 'no SLO set'} tone={p99Tone} />
        <Stat label="Errors" value={r.errorRate !== undefined ? `${fmt(r.errorRate * 100, 3)}%` : '–'} sub={`budget ${fmt((slo?.errorRate ?? 0.001) * 100, 3)}%`} tone={r.errorRate !== undefined && r.errorRate > (slo?.errorRate ?? 0.001) ? 'bad' : undefined} />
        <Stat
          label="Max sustainable"
          value={result.maxSustainableTps === null ? '∞' : `${fmt(result.maxSustainableTps)} TPS`}
          sub={result.maxSustainableTps === null ? 'no finite bottleneck' : `limited by ${limitLabel(result.limitedBy)}`}
        />
        <Stat label="Confidence" value={r.confidence.level} sub={`score ${fmt(r.confidence.score, 2)}`} tone={r.confidence.level === 'low' ? 'warn' : undefined} />
      </div>
      {r.confidence.criticalDefaults?.length ? <p className="rounded-md border border-warn/40 bg-warn-soft px-3 py-2 text-[12px]">{r.confidence.summary}</p> : null}

      <div className="grid gap-4 xl:grid-cols-2">
        <div>
          <h3 className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Resources at target</h3>
          <table className="w-full text-[12px]">
            <tbody>
              {(r.resources ?? []).map((x) => (
                <tr key={x.id} className="border-t border-line">
                  <td className="max-w-0 py-1 pr-2">
                    <button type="button" className="block w-full truncate text-left font-mono text-[11.5px] hover:text-accent" title={x.note ?? x.id} onClick={() => selectRef(x.node)}>
                      {x.id}
                    </button>
                  </td>
                  <td className="w-[45%] py-1">
                    <UtilBar u={x.utilization} warn={x.scalingHeadroom ? overload : warn} overload={overload} />
                  </td>
                  <td className="num w-28 py-1 pl-2 text-right text-[11px] whitespace-nowrap text-muted">
                    {fmt(x.load)} / {fmt(x.capacity)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {result.saturationOrder?.length ? (
            <p className="mt-2 text-[11.5px] text-muted">
              Saturates first as TPS rises: {result.saturationOrder.map((x) => x.id).join(' → ')}
            </p>
          ) : null}
        </div>

        <div>
          <h3 className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Latency per request (mean)</h3>
          <HopChart result={result} />
        </div>
      </div>

      {r.replicas?.length || r.nodePools?.length ? (
        <div>
          <h3 className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Replicas & nodes</h3>
          <div className="flex flex-wrap gap-2">
            {r.replicas?.map((x) => (
              <span key={x.service} className="rounded-md border border-line px-2 py-1 text-[11.5px]">
                <RefLink refId={x.host ?? x.service} />{' '}
                <span className="num">
                  {x.scheduled}
                  {x.pending ? <span className="text-bad"> +{x.pending} pending</span> : null}
                </span>
                {x.autoscaler && <span className="text-muted"> · {x.autoscaler.toUpperCase()} {x.min}–{x.max}</span>}
                {x.consuming ? <span className="text-warn"> · {x.consuming} consuming</span> : null}
              </span>
            ))}
            {r.nodePools?.map((p) => (
              <span key={p.node} className="rounded-md border border-line px-2 py-1 text-[11.5px]">
                <RefLink refId={p.node} /> <span className="num">{p.nodesNeeded}/{p.nodesAvailable} nodes</span>
                <span className="text-muted"> · {fmt(p.allocatableCpuPerNode, 2)} vCPU allocatable</span>
              </span>
            ))}
          </div>
        </div>
      ) : null}

      {r.issues?.length ? (
        <div>
          <h3 className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Issues</h3>
          <ul className="space-y-1 text-[12px]">
            {r.issues.map((i, k) => (
              <li key={k} className="flex gap-2">
                <span className={cx('w-14 shrink-0 font-medium uppercase', severityCls[i.severity])}>{i.severity}</span>
                <span>
                  <RefLink refId={i.ref} />: {i.message}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <Assumptions list={r.assumptions} />
    </div>
  )
}

function HopChart({ result }: { result: Result }) {
  const hops = (result.target.hops ?? []).filter((h) => h.networkMs + h.waitMs + h.serviceMs + h.delayMs > 0)
  if (!hops.length) return <p className="text-[12px] text-muted">{result.target.stable ? 'No latency contributors.' : 'Unavailable: the system is unstable at this load.'}</p>
  const max = Math.max(...hops.map((h) => h.networkMs + h.waitMs + h.serviceMs + h.delayMs))
  const parts = [
    { key: 'networkMs', label: 'network', color: '#7c8799' },
    { key: 'waitMs', label: 'queue wait', color: 'var(--bad)' },
    { key: 'serviceMs', label: 'service', color: 'var(--accent)' },
    { key: 'delayMs', label: 'delay / IO', color: '#0f9d8a' },
  ] as const
  return (
    <div>
      <div className="space-y-1">
        {hops.map((h) => {
          const total = h.networkMs + h.waitMs + h.serviceMs + h.delayMs
          return (
            <div key={h.node} className="grid grid-cols-[110px_1fr_64px] items-center gap-2 text-[11.5px]">
              <button type="button" className="truncate text-left font-mono hover:text-accent" onClick={() => selectRef(h.node)}>
                {h.node}
              </button>
              <div className="flex h-3 overflow-hidden rounded-sm bg-panel-2" style={{ width: `${Math.max(2, (total / max) * 100)}%` }} title={parts.map((p) => `${p.label} ${fmtMs(h[p.key])}`).join(' · ')}>
                {parts.map((p) => (
                  <div key={p.key} style={{ width: `${(h[p.key] / total) * 100}%`, background: p.color }} />
                ))}
              </div>
              <span className="num text-right text-muted">{fmtMs(total)}</span>
            </div>
          )
        })}
      </div>
      <div className="mt-2 flex flex-wrap gap-3 text-[10.5px] text-muted">
        {parts.map((p) => (
          <span key={p.key} className="inline-flex items-center gap-1">
            <span className="h-2 w-2 rounded-sm" style={{ background: p.color }} />
            {p.label}
          </span>
        ))}
      </div>
    </div>
  )
}

function Assumptions({ list }: { list: Assumption[] }) {
  const [onlyDefaults, setOnlyDefaults] = useState(true)
  const shown = onlyDefaults ? list.filter((a) => a.source.kind !== 'user') : list
  return (
    <details className="rounded-lg border border-line">
      <summary className="cursor-pointer px-3 py-2 text-[11px] font-semibold tracking-wide text-muted uppercase">
        Assumptions ({list.filter((a) => a.source.kind !== 'user').length} defaults used)
      </summary>
      <div className="border-t border-line px-3 py-2">
        <label className="mb-2 flex items-center gap-2 text-[11.5px] text-muted">
          <input type="checkbox" checked={onlyDefaults} onChange={(e) => setOnlyDefaults(e.target.checked)} /> only values not set by you
        </label>
        <table className="w-full text-[11.5px]">
          <tbody>
            {shown.map((a) => (
              <tr key={`${a.node}/${a.param}`} className="border-t border-line">
                <td className="py-1 pr-2 font-mono">
                  <RefLink refId={a.node} />
                </td>
                <td className="py-1 pr-2 font-mono">{a.param}</td>
                <td className="num max-w-[240px] truncate py-1 pr-2" title={JSON.stringify(a.value)}>
                  {typeof a.value === 'object' ? JSON.stringify(a.value) : String(a.value)}
                </td>
                <td className="py-1 text-right">
                  <SourceBadge source={a.source} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  )
}

export function EngineErrorView({ message, stale }: { message: string; stale?: boolean }) {
  return (
    <div className={cx('rounded-lg border border-bad/40 bg-bad-soft px-3 py-2.5 text-[12px]', !stale && 'm-4')}>
      <div className="mb-1 font-semibold text-bad">{stale ? 'The latest change made the design invalid; showing the previous result.' : 'The design cannot be simulated yet'}</div>
      <pre className="font-mono text-[11.5px] whitespace-pre-wrap">{message}</pre>
    </div>
  )
}
