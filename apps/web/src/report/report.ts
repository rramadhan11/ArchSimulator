// Printable report of a design and its latest results: a self-contained HTML
// page (no scripts) the browser can print or save as PDF. Every value from
// the design is escaped: names come from user input or shared links.
import type { Architecture, Assumption, Cost, Result, Timeline } from '../engine/types'

export interface ReportInput {
  name: string
  architecture: Architecture
  check?: Result
  timeline?: Timeline
  baseline?: { name: string; check?: Result; timeline?: Timeline }
  generatedAt: Date
}

const esc = (v: unknown) =>
  String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)

const num = (n: number | null | undefined, digits = 0) =>
  n === null || n === undefined || !Number.isFinite(n) ? '–' : n.toLocaleString('en-US', { maximumFractionDigits: digits })
const ms = (n: number | null | undefined) => (n === null || n === undefined ? '–' : n >= 10000 ? `${num(n / 1000, 1)} s` : `${num(n, n < 10 ? 2 : n < 100 ? 1 : 0)} ms`)
const pct = (n: number | null | undefined, digits = 1) => (n === null || n === undefined ? '–' : `${num(n * 100, digits)}%`)

function table(head: string[], rows: string[][]): string {
  if (!rows.length) return ''
  return `<table><thead><tr>${head.map((h) => `<th>${esc(h)}</th>`).join('')}</tr></thead><tbody>${rows
    .map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join('')}</tr>`)
    .join('')}</tbody></table>`
}

/** A small line chart as inline SVG: one or more series over the run's seconds. */
function chart(title: string, series: { name: string; color: string; values: (number | null)[]; dashed?: boolean }[], unit: string, spans: [number, number][] = []): string {
  const W = 680
  const H = 150
  const pad = { l: 44, r: 8, t: 8, b: 20 }
  const n = Math.max(...series.map((s) => s.values.length), 1)
  const max = Math.max(1e-9, ...series.flatMap((s) => s.values.filter((v): v is number => v !== null)))
  const x = (i: number) => pad.l + (i / Math.max(n - 1, 1)) * (W - pad.l - pad.r)
  const y = (v: number) => pad.t + (1 - v / max) * (H - pad.t - pad.b)
  const path = (vals: (number | null)[]) => {
    let d = ''
    let pen = false
    vals.forEach((v, i) => {
      if (v === null) {
        pen = false
        return
      }
      d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`
      pen = true
    })
    return d
  }
  const grid = [0, 0.5, 1]
    .map((f) => `<line x1="${pad.l}" x2="${W - pad.r}" y1="${y(max * f)}" y2="${y(max * f)}" class="grid"/><text x="${pad.l - 4}" y="${y(max * f) + 3}" text-anchor="end">${esc(num(max * f, max < 10 ? 1 : 0))}</text>`)
    .join('')
  const shaded = spans.map(([a, b]) => `<rect x="${x(a)}" y="${pad.t}" width="${Math.max(1, x(b) - x(a))}" height="${H - pad.t - pad.b}" class="fail"/>`).join('')
  const lines = series.map((s) => `<path d="${path(s.values)}" stroke="${s.color}" ${s.dashed ? 'stroke-dasharray="4 3"' : ''} fill="none" stroke-width="1.5"/>`).join('')
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => `<text x="${x(f * (n - 1))}" y="${H - 5}" text-anchor="middle">${Math.round(f * (n - 1))}s</text>`).join('')
  const legend = series.map((s) => `<span><i style="background:${s.color}"></i>${esc(s.name)}</span>`).join('')
  return `<figure><figcaption>${esc(title)} <small>(${esc(unit)})</small></figcaption><div class="legend">${legend}</div><svg viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(title)}">${shaded}${grid}${lines}${ticks}</svg></figure>`
}

function assumptions(list: Assumption[]): string {
  return table(
    ['Component', 'Parameter', 'Value', 'Source'],
    list.map((a) => [
      esc(a.node),
      `<code>${esc(a.param)}</code>`,
      esc(typeof a.value === 'object' ? JSON.stringify(a.value) : a.value),
      `${esc(a.source.kind)}${a.source.note ? ` — ${esc(a.source.note)}` : ''}${a.source.url ? ` <span class="url">${esc(a.source.url)}</span>` : ''}`,
    ]),
  )
}

function quickCheck(r: Result, base?: Result): string {
  const t = r.target
  const compare = base
    ? `<h3>Compared with the baseline</h3>${table(
        ['', 'Baseline', 'This design'],
        [
          ['Verdict', esc(base.target.verdict), esc(t.verdict)],
          ['p99', ms(base.target.latency?.p99Ms), ms(t.latency?.p99Ms)],
          ['Error rate', pct(base.target.errorRate, 3), pct(t.errorRate, 3)],
          ['Max sustainable', base.maxSustainableTps === null ? '∞' : `${num(base.maxSustainableTps)} TPS`, r.maxSustainableTps === null ? '∞' : `${num(r.maxSustainableTps)} TPS`],
        ],
      )}`
    : ''
  return `
<h2>Quick check at ${esc(num(t.tps))} TPS</h2>
<p class="verdict ${esc(t.verdict)}">${esc(t.verdict.toUpperCase())}</p>
${t.reasons?.length ? `<ul>${t.reasons.map((x) => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}
${table(
  ['p50', 'p95', 'p99', 'Error rate', 'Max sustainable', 'Limited by', 'Confidence'],
  [
    [
      ms(t.latency?.p50Ms),
      ms(t.latency?.p95Ms),
      ms(t.latency?.p99Ms),
      pct(t.errorRate, 3),
      r.maxSustainableTps === null ? '∞' : `${num(r.maxSustainableTps)} TPS`,
      esc(r.limitedBy),
      `${esc(t.confidence.level)} (${num(t.confidence.score, 2)})`,
    ],
  ],
)}
<p class="note">${esc(t.confidence.summary)}</p>
${compare}
<h3>Resources at target</h3>
${table(
  ['Resource', 'Utilization', 'Load', 'Capacity', 'Note'],
  (t.resources ?? []).map((x) => [`<code>${esc(x.id)}</code>`, pct(x.utilization, 0), `${num(x.load)} ${esc(x.unit)}`, num(x.capacity), esc(x.note)]),
)}
${r.saturationOrder?.length ? `<p>Saturates first as TPS rises: ${r.saturationOrder.map((x) => `<code>${esc(x.id)}</code>`).join(' → ')}</p>` : ''}
<h3>Latency per hop (mean per request)</h3>
${table(
  ['Component', 'Visits', 'Network', 'Queue wait', 'Service', 'Delay'],
  (t.hops ?? []).map((h) => [esc(h.node), num(h.visitsPerRequest, 2), ms(h.networkMs), ms(h.waitMs), ms(h.serviceMs), ms(h.delayMs)]),
)}
${t.cost ? costSection(t.cost) : ''}
${t.issues?.length ? `<h3>Issues</h3><ul>${t.issues.map((i) => `<li><b>${esc(i.severity)}</b> <code>${esc(i.ref)}</code>: ${esc(i.message)}</li>`).join('')}</ul>` : ''}`
}

const usd = (n: number | null | undefined, digits = 0) => (n === null || n === undefined ? '–' : `$${num(n, digits)}`)

/** Exported for tests. */
export function costSection(c: Cost): string {
  return `
<h3>Cost (on-demand, instances only)</h3>
${table(
  ['Component', 'Instances', 'Rate', 'Per month (730 h)'],
  [
    ...c.items.map((it) => [
      `<code>${esc(it.node)}</code>`,
      `${num(it.count)} × <code>${esc(it.rate)}</code>`,
      it.hourlyUSD === null ? 'no rate set' : `${usd(it.hourlyUSD, 4)}/h${it.source === 'yours' ? ' (design rate)' : ''}`,
      usd(it.monthlyUSD),
    ]),
    ['<b>Total</b>', '', '', `<b>${usd(c.monthlyUSD)}</b>${c.unpriced?.length ? ` + ${esc(c.unpriced.join(', '))} (no rate)` : ''}`],
  ],
)}
<p class="note">Load balancers, queues, caches, external APIs, storage and traffic are not included.</p>`
}

function timeline(tl: Timeline, base?: Timeline): string {
  const spans: [number, number][] = (tl.failures ?? []).map((f) => [f.startS, f.endS ?? tl.ticks.length - 1])
  const s = tl.summary
  const b = base?.summary
  const aligned = (pick: (k: Timeline['ticks'][number]) => number | null) =>
    base ? tl.ticks.map((_, i) => (i < base.ticks.length ? pick(base.ticks[i]) : null)) : []
  return `
<h2>Time simulation</h2>
${table(
  ['', 'Peak p99', 'Max error rate', 'SLO broken', 'First breach', 'Cost of the window'],
  [
    ['This design', ms(s.peakP99Ms), pct(s.maxErrorRate, 2), `${num(s.sloBreachS)} s`, s.firstBreachS === null ? '–' : `t = ${num(s.firstBreachS)} s`, usd(s.costUSD, 2)],
    ...(b ? [['Baseline', ms(b.peakP99Ms), pct(b.maxErrorRate, 2), `${num(b.sloBreachS)} s`, b.firstBreachS === null ? '–' : `t = ${num(b.firstBreachS)} s`, usd(b.costUSD, 2)]] : []),
  ],
)}
${tl.failures?.length ? `<h3>Injected failures</h3><ul>${tl.failures.map((f) => `<li>t = ${num(f.startS)} s: <code>${esc(f.target ?? 'zone')}</code> ${esc(f.label)}${f.endS === null ? ' (until the end)' : f.endS > f.startS ? ` until ${num(f.endS)} s` : ''}</li>`).join('')}</ul>` : ''}
${chart('Offered load', [{ name: 'offered TPS', color: '#3b7ae8', values: tl.ticks.map((k) => k.offeredTps) }], 'requests/s', spans)}
${chart(
  'p99 latency',
  [
    { name: 'p99', color: '#0f9d8a', values: tl.ticks.map((k) => k.latency?.p99Ms ?? null) },
    ...(base ? [{ name: 'baseline p99', color: '#7c8799', dashed: true, values: aligned((k) => k.latency?.p99Ms ?? null) }] : []),
  ],
  'ms',
  spans,
)}
${chart(
  'Error rate',
  [
    { name: 'errors', color: '#e0533d', values: tl.ticks.map((k) => k.errorRate * 100) },
    ...(base ? [{ name: 'baseline errors', color: '#7c8799', dashed: true, values: aligned((k) => k.errorRate * 100) }] : []),
  ],
  '%',
  spans,
)}
${chart(
  'Ready replicas',
  tl.services.map((svc, i) => ({ name: svc, color: ['#3b7ae8', '#d98a0b', '#8b5cf6', '#0ea5b7', '#c2417f'][i % 5], values: tl.ticks.map((k) => k.replicas[i].ready) })),
  'replicas',
  spans,
)}
${tl.events?.length ? `<h3>Events</h3>${table(['t', 'Component', 'Event'], tl.events.map((e) => [`${num(e.t)} s`, `<code>${esc(e.ref)}</code>`, esc(e.message)]))}` : ''}`
}

export function buildReport(r: ReportInput): string {
  const a = r.architecture
  const w = a.workload
  const classes = w.classes?.map((c) => `${esc(c.name)} ${num(c.weight, 2)}`).join(', ')
  const assumed = r.check?.target.assumptions ?? r.timeline?.assumptions ?? []
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(r.name)} — ArchSim report</title>
<style>
  body { font: 13px/1.5 ui-sans-serif, system-ui, sans-serif; color: #1c2230; max-width: 920px; margin: 32px auto; padding: 0 24px; }
  h1 { font-size: 22px; margin: 0 0 4px; } h2 { font-size: 16px; margin: 28px 0 8px; border-bottom: 1px solid #dde2ea; padding-bottom: 4px; }
  h3 { font-size: 13px; margin: 18px 0 6px; } .meta, .note, small { color: #667085; }
  table { border-collapse: collapse; width: 100%; margin: 6px 0 10px; font-size: 12px; } th, td { text-align: left; padding: 4px 8px; border-bottom: 1px solid #e7ebf1; vertical-align: top; }
  th { color: #667085; font-weight: 600; } code { font: 11.5px ui-monospace, monospace; } .url { color: #667085; word-break: break-all; }
  .verdict { display: inline-block; font-weight: 700; padding: 3px 10px; border-radius: 6px; } .safe { background: #e5f6ef; color: #0d7a55; } .warning { background: #fdf3df; color: #9a6205; } .overload { background: #fde8e5; color: #b4321f; }
  figure { margin: 12px 0; break-inside: avoid; } figcaption { font-weight: 600; font-size: 12px; } svg { width: 100%; height: auto; }
  svg text { font-size: 9px; fill: #667085; } .grid { stroke: #e7ebf1; } .fail { fill: #e0533d; opacity: .08; }
  .legend { font-size: 11px; color: #667085; } .legend span { margin-right: 12px; } .legend i { display: inline-block; width: 10px; height: 3px; margin-right: 4px; vertical-align: middle; }
  .print-hint { background: #eef3fd; padding: 8px 12px; border-radius: 6px; } @media print { .print-hint { display: none; } body { margin: 0; } }
</style></head><body>
<p class="print-hint">Print this page (Ctrl/Cmd+P) and choose “Save as PDF” to get a PDF.</p>
<h1>${esc(r.name)}</h1>
<p class="meta">ArchSim report · ${esc(r.generatedAt.toLocaleString())} · catalog ${esc(r.check?.catalogVersion ?? a.catalogVersion)}${r.baseline ? ` · compared with “${esc(r.baseline.name)}”` : ''}</p>

<h2>Design</h2>
<p>Entry <code>${esc(w.entry)}</code> at ${esc(num(w.targetTps))} TPS${classes ? ` (classes: ${classes})` : ''}; SLO p99 ${w.slo?.p99Ms ? ms(w.slo.p99Ms) : 'not set'}, error budget ${pct(w.slo?.errorRate ?? 0.001, 3)}.</p>
${table(
  ['Component', 'Kind', 'Placed in'],
  a.nodes.map((n) => [`<code>${esc(n.id)}</code>${n.name ? ` ${esc(n.name)}` : ''}`, esc(n.kind), n.parent ? `<code>${esc(n.parent)}</code>` : '']),
)}
${table(
  ['Call', 'Protocol', 'Timeout', 'Retry', 'Circuit breaker'],
  a.edges.map((e) => [
    `<code>${esc(e.from)} → ${esc(e.to)}</code>`,
    esc(e.protocol ?? 'default'),
    e.timeoutMs ? ms(e.timeoutMs) : '',
    e.retry ? `${esc(e.retry.max)}× / ${ms(e.retry.backoffMs)}${e.retry.budgetPercent !== undefined ? ` / budget ${esc(e.retry.budgetPercent)}%` : ''}` : '',
    e.circuitBreaker ? 'yes' : '',
  ]),
)}

${r.check ? quickCheck(r.check, r.baseline?.check) : '<p class="note">No quick check result.</p>'}
${r.timeline ? timeline(r.timeline, r.baseline?.timeline) : '<h2>Time simulation</h2><p class="note">Not run for this report.</p>'}

<h2>Assumptions</h2>
<p class="note">Every value the results depend on, with its source. “default-estimate” values are rough guesses: replace the critical ones with measurements.</p>
${assumptions(assumed)}

<h2>Method and limits</h2>
<p class="note">Quick check: steady-state queueing model (M/M/c with Kingman correction, Universal Scalability Law), steady-state autoscaling and pod scheduling, and seeded Monte Carlo sampling for latency percentiles. Time simulation: 1-second steps with fluid backlogs, Kubernetes/VM autoscaler control loops, provisioning delays, retries, circuit breakers and injected failures. Not modeled: memory pressure, threads held by hung dependencies, retries in the quick check. Results are estimates whose accuracy depends on the parameters above.</p>
</body></html>`
}
