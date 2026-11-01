import { useMemo } from 'react'
import type { LoadPattern, Timeline } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { Chart, themeColors, type ChartOption } from '../ui/Chart'
import { Button, cx, fmt, fmtMs } from '../ui/kit'
import { EngineErrorView } from './QuickCheck'

const MAX_STATION_SERIES = 6

function describeLoad(base: number, load: LoadPattern | undefined): string {
  const peak = fmt(load?.peakTps ?? 0)
  const at = `at t=${load?.startS ?? 0}s`
  switch (load?.kind) {
    case 'spike':
      return `spike ${fmt(base)} → ${peak} TPS ${at} (ramp ${load.rampS ?? 0}s, hold ${load.holdS ?? 0}s)`
    case 'ramp':
      return `ramp ${fmt(base)} → ${peak} TPS ${at} over ${load.rampS ?? 0}s`
    case 'step':
      return `step ${fmt(base)} → ${peak} TPS ${at}${load.holdS ? ` for ${load.holdS}s` : ''}`
    case 'diurnal':
      return `diurnal ${fmt(base)} ↔ ${peak} TPS, period ${load.periodS ?? 0}s`
    default:
      return `constant ${fmt(base)} TPS`
  }
}

export function TimelineView() {
  const status = useDesign((s) => s.timeline)
  const w = useDesign((s) => s.workload)
  const baseline = useDesign((s) => s.baseline)
  const tl = latest(status)
  const base = baseline ? latest(baseline.timeline) : undefined
  const failures = w.timeline?.failures?.length ?? 0

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void useDesign.getState().runTimeline()} disabled={status.state === 'running'}>
          {status.state === 'running' ? 'Simulating…' : tl ? 'Re-run simulation' : 'Run time simulation'}
        </Button>
        <span className="text-[12px] text-muted">
          {describeLoad(w.targetTps, w.timeline?.load)} for {w.timeline?.durationS ?? 600}s, 1 s ticks
          {failures ? `, ${failures} injected failure${failures > 1 ? 's' : ''}` : ''}. Edit under Workload (click the empty canvas).
        </span>
        {status.state === 'done' && <span className="num ml-auto text-[10.5px] text-muted">computed in {fmt(status.ms)} ms</span>}
      </div>
      {status.state === 'error' && <EngineErrorView message={status.message} stale={!!tl} />}
      {baseline && baseline.timeline.state === 'running' && <p className="text-[11.5px] text-muted">Simulating baseline “{baseline.saved.name}”…</p>}
      {baseline && baseline.timeline.state === 'error' && <p className="text-[11.5px] text-bad">Baseline “{baseline.saved.name}” failed: {baseline.timeline.message}</p>}
      {tl && <TimelineResult tl={tl} base={base} baseName={baseline?.saved.name} sloP99={w.slo?.p99Ms} running={status.state === 'running'} />}
      {!tl && status.state !== 'error' && (
        <p className="max-w-2xl text-[12px] text-muted">
          Steps the design once per second: HPA / VM autoscaler control loops, pod startup, Pending pods, node provisioning and scale-down, backlogs when demand exceeds capacity, retries (with backoff and budgets) and circuit
          breakers. Inject failures under Workload to see how the design degrades and whether it recovers.
        </p>
      )}
    </div>
  )
}

function TimelineResult({ tl, base, baseName, sloP99, running }: { tl: Timeline; base?: Timeline; baseName?: string; sloP99?: number; running: boolean }) {
  const s = tl.summary
  const b = base?.summary
  const charts = useMemo(() => buildCharts(tl, base, sloP99), [tl, base, sloP99])
  const p99 = (x: number | null) => (x === null ? '–' : fmtMs(x))
  return (
    <div className={cx('space-y-4 transition-opacity', running && 'opacity-60')}>
      <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        <Tile label="Peak p99" value={p99(s.peakP99Ms)} was={b && p99(b.peakP99Ms)} tone={sloP99 && s.peakP99Ms !== null && s.peakP99Ms > sloP99 ? 'bad' : undefined} />
        <Tile label="Max error rate" value={`${fmt(s.maxErrorRate * 100, 2)}%`} was={b && `${fmt(b.maxErrorRate * 100, 2)}%`} tone={s.maxErrorRate > 0 ? 'bad' : undefined} />
        <Tile label="SLO broken" value={s.sloBreachS ? `${fmt(s.sloBreachS)} s` : 'never'} was={b && (b.sloBreachS ? `${fmt(b.sloBreachS)} s` : 'never')} tone={s.sloBreachS ? 'bad' : 'ok'} />
        <Tile label="First breach" value={s.firstBreachS === null ? '–' : `t = ${fmt(s.firstBreachS)} s`} was={b && (b.firstBreachS === null ? '–' : `t = ${fmt(b.firstBreachS)} s`)} />
      </div>
      {base && <p className="text-[11px] text-muted">Grey dashed lines and “was” values: baseline “{baseName}”.</p>}
      <div className="grid gap-4 xl:grid-cols-[1fr_340px]">
        <div className="space-y-1">
          {charts.map((c) => (
            <div key={c.title}>
              <div className="text-[11px] font-semibold tracking-wide text-muted uppercase">{c.title}</div>
              <Chart option={c.option} height={150} group="timeline" />
            </div>
          ))}
        </div>
        <div>
          {tl.failures?.length ? (
            <>
              <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Injected failures</div>
              <ul className="mb-3 space-y-1 text-[11.5px]">
                {tl.failures.map((f, i) => (
                  <li key={i} className="grid grid-cols-[48px_1fr] gap-2">
                    <span className="num text-muted">{fmt(f.startS)}s</span>
                    <span>
                      {f.target && <span className="font-mono">{f.target}</span>} {f.label}
                      {f.endS === null ? ' (until the end)' : f.endS > f.startS ? ` until ${fmt(f.endS)}s` : ''}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          ) : null}
          <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Events</div>
          <ol className="max-h-[640px] space-y-1 overflow-y-auto text-[11.5px]">
            {(tl.events ?? []).map((e, i) => (
              <li key={i} className="grid grid-cols-[48px_1fr] gap-2 border-t border-line pt-1">
                <span className="num text-muted">{fmt(e.t)}s</span>
                <span>
                  <span className="font-mono">{e.ref}</span> {e.message}
                </span>
              </li>
            ))}
            {!tl.events?.length && <li className="text-muted">Nothing happened: no scaling, no backlog.</li>}
          </ol>
          {tl.issues?.length ? (
            <ul className="mt-3 space-y-1 text-[11px] text-muted">
              {tl.issues.map((i, k) => (
                <li key={k}>
                  <span className="font-mono">{i.ref}</span>: {i.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      </div>
    </div>
  )
}

function Tile({ label, value, was, tone }: { label: string; value: string; was?: string; tone?: 'ok' | 'bad' }) {
  return (
    <div className="rounded-lg border border-line bg-panel px-3 py-2">
      <div className="text-[10.5px] font-medium tracking-wide text-muted uppercase">{label}</div>
      <div className={cx('num text-[16px] font-semibold', tone === 'ok' && 'text-ok', tone === 'bad' && 'text-bad')}>{value}</div>
      {was !== undefined && <div className="num text-[11px] text-muted">was {was}</div>}
    </div>
  )
}

function buildCharts(tl: Timeline, base: Timeline | undefined, sloP99?: number): { title: string; option: ChartOption }[] {
  const c = themeColors()
  const n = tl.ticks.length
  const t = tl.ticks.map((k) => k.t)
  const grid = { left: 48, right: 48, top: 28, bottom: 22 }
  const xAxis = { type: 'category', data: t, boundaryGap: false, axisLine: { lineStyle: { color: c.line } }, axisLabel: { formatter: '{value}s' } }
  // Units live in the series names: axis names would collide with the legend.
  const yAxis = (extra: object = {}) => ({ type: 'value', splitLine: { lineStyle: { color: c.line } }, ...extra })
  const line = (name: string, data: (number | null)[], extra: object = {}) => ({ name, type: 'line', data, showSymbol: false, lineStyle: { width: 1.5 }, ...extra })
  const legend = { top: 0, left: 40, textStyle: { color: c.muted, fontSize: 10.5 }, itemWidth: 12, itemHeight: 6 }
  // The baseline may have run for a different duration: align it to this run's ticks.
  const baseLine = (name: string, pick: (k: Timeline['ticks'][number]) => number | null, extra: object = {}) =>
    line(`baseline ${name}`, Array.from({ length: n }, (_, i) => (base && i < base.ticks.length ? pick(base.ticks[i]) : null)), {
      lineStyle: { width: 1, type: 'dashed', color: c.muted },
      itemStyle: { color: c.muted },
      ...extra,
    })

  // Failure windows shade every chart; one-off failures are vertical lines.
  const spans = tl.failures ?? []
  const failureArea = {
    silent: true,
    itemStyle: { color: c.bad, opacity: 0.07 },
    label: { show: false },
    data: spans.filter((f) => f.endS !== f.startS).map((f) => [{ xAxis: f.startS }, { xAxis: Math.min(f.endS ?? n - 1, n - 1) }]),
  }
  const failureLines = {
    silent: true,
    symbol: 'none',
    lineStyle: { color: c.bad, type: 'dotted' },
    label: { show: false },
    data: spans.filter((f) => f.endS === f.startS).map((f) => ({ xAxis: f.startS })),
  }
  const marks = (extra: object = {}) => ({ markArea: failureArea, markLine: failureLines, ...extra })

  // Busiest stations by peak load keep the chart readable.
  const stationPeak = tl.stations.map((id, k) => ({ id, k, peak: Math.max(0, ...tl.ticks.map((x) => x.stations[k].load ?? 10)) }))
  const top = stationPeak.sort((a, b) => b.peak - a.peak).slice(0, MAX_STATION_SERIES)

  const charts: { title: string; option: ChartOption }[] = [
    {
      title: 'Load & latency',
      option: {
        grid,
        legend,
        xAxis,
        yAxis: [yAxis(), yAxis({ splitLine: { show: false } })],
        series: [
          line('offered TPS', tl.ticks.map((k) => k.offeredTps), { areaStyle: { opacity: 0.08 }, ...marks() }),
          line('p50 ms', tl.ticks.map((k) => k.latency?.p50Ms ?? null), { yAxisIndex: 1 }),
          line('p99 ms', tl.ticks.map((k) => k.latency?.p99Ms ?? null), {
            yAxisIndex: 1,
            ...(sloP99 ? { markLine: { silent: true, symbol: 'none', data: [{ yAxis: sloP99, name: 'SLO' }], lineStyle: { color: c.bad, type: 'dashed' }, label: { formatter: 'SLO', color: c.bad } } } : {}),
          }),
          ...(base ? [baseLine('p99 ms', (k) => k.latency?.p99Ms ?? null, { yAxisIndex: 1 })] : []),
        ],
      },
    },
    {
      title: 'Replicas (ready solid, desired dashed) · nodes on the right axis',
      option: {
        grid,
        legend,
        xAxis,
        yAxis: [yAxis({ minInterval: 1 }), yAxis({ minInterval: 1, splitLine: { show: false } })],
        series: [
          ...tl.services.flatMap((svc, k) => [
            line(`${svc} ready`, tl.ticks.map((x) => x.replicas[k].ready), { step: 'end', ...(k === 0 ? marks() : {}) }),
            line(`${svc} desired`, tl.ticks.map((x) => x.replicas[k].desired), { step: 'end', lineStyle: { width: 1, type: 'dashed' } }),
          ]),
          ...(tl.pools ?? []).map((p, k) => line(`${p} nodes`, tl.ticks.map((x) => x.pools![k].ready), { step: 'end', yAxisIndex: 1, lineStyle: { width: 1, type: 'dotted' } })),
        ],
      },
    },
    {
      title: 'Station load (offered work / capacity)',
      option: {
        grid,
        legend,
        xAxis,
        yAxis: yAxis({ axisLabel: { formatter: '{value}%' } }),
        series: top.map(({ id, k }, i) =>
          line(id, tl.ticks.map((x) => (x.stations[k].load === null ? null : Math.round(x.stations[k].load! * 1000) / 10)), {
            ...(i === 0 ? marks({ markLine: { ...failureLines, data: [...failureLines.data, { yAxis: 100, lineStyle: { color: c.bad, type: 'dashed' } }] } }) : {}),
          }),
        ),
      },
    },
    {
      title: 'Backlog wait & errors',
      option: {
        grid,
        legend,
        xAxis,
        yAxis: [yAxis({ axisLabel: { formatter: '{value}s' } }), yAxis({ max: 100, splitLine: { show: false }, axisLabel: { formatter: '{value}%' } })],
        series: [
          ...top.map(({ id, k }, i) => line(`${id} wait`, tl.ticks.map((x) => x.stations[k].waitMs / 1000), i === 0 ? marks() : {})),
          line('error rate %', tl.ticks.map((x) => Math.round(x.errorRate * 10000) / 100), { yAxisIndex: 1, lineStyle: { width: 1.5, color: c.bad }, itemStyle: { color: c.bad }, areaStyle: { opacity: 0.1, color: c.bad } }),
          ...(base ? [baseLine('error rate %', (k) => Math.round(k.errorRate * 10000) / 100, { yAxisIndex: 1 })] : []),
        ],
      },
    },
  ]

  if (tl.edges?.length) {
    // Breaker-open periods (any edge) shade the chart in the warning color.
    const open: { xAxis: number }[][] = []
    tl.edges.forEach((_, k) => {
      let from = -1
      tl.ticks.forEach((x, i) => {
        const isOpen = x.edges?.[k]?.breaker === 'open'
        if (isOpen && from < 0) from = i
        if ((!isOpen || i === n - 1) && from >= 0) {
          open.push([{ xAxis: from }, { xAxis: isOpen ? i : i - 1 }])
          from = -1
        }
      })
    })
    charts.push({
      title: `Calls on resilience edges (retries dashed${open.length ? ', breaker open shaded' : ''})`,
      option: {
        grid,
        legend,
        xAxis,
        yAxis: [yAxis(), yAxis({ max: 100, splitLine: { show: false }, axisLabel: { formatter: '{value}%' } })],
        series: tl.edges.flatMap((id, k) => [
          line(`${id} attempts/s`, tl.ticks.map((x) => x.edges?.[k]?.attempts ?? null), k === 0 ? { markArea: { silent: true, itemStyle: { color: c.warn, opacity: 0.12 }, data: open } } : {}),
          line(`${id} retries/s`, tl.ticks.map((x) => x.edges?.[k]?.retries ?? null), { lineStyle: { width: 1, type: 'dashed' } }),
          line(`${id} failed %`, tl.ticks.map((x) => (x.edges?.[k] ? Math.round(x.edges[k].failRate * 1000) / 10 : null)), { yAxisIndex: 1, lineStyle: { width: 1, type: 'dotted' } }),
        ]),
      },
    })
  }
  return charts
}
