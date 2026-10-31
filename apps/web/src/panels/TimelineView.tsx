import { useMemo } from 'react'
import type { Timeline } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { Chart, themeColors, type ChartOption } from '../ui/Chart'
import { Button, cx, fmt, fmtMs } from '../ui/kit'
import { EngineErrorView } from './QuickCheck'

const MAX_STATION_SERIES = 6

export function TimelineView() {
  const status = useDesign((s) => s.timeline)
  const w = useDesign((s) => s.workload)
  const tl = latest(status)
  const load = w.timeline?.load
  const pattern =
    load?.kind === 'spike'
      ? `spike ${fmt(w.targetTps)} → ${fmt(load.peakTps ?? 0)} TPS at t=${load.startS ?? 0}s (ramp ${load.rampS ?? 0}s, hold ${load.holdS ?? 0}s)`
      : `constant ${fmt(w.targetTps)} TPS`

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" onClick={() => void useDesign.getState().runTimeline()} disabled={status.state === 'running'}>
          {status.state === 'running' ? 'Simulating…' : tl ? 'Re-run simulation' : 'Run time simulation'}
        </Button>
        <span className="text-[12px] text-muted">
          {pattern} for {w.timeline?.durationS ?? 600}s, 1 s ticks. Edit under Workload (click the empty canvas).
        </span>
        {status.state === 'done' && <span className="num ml-auto text-[10.5px] text-muted">computed in {fmt(status.ms)} ms</span>}
      </div>
      {status.state === 'error' && <EngineErrorView message={status.message} stale={!!tl} />}
      {tl && <TimelineResult tl={tl} sloP99={w.slo?.p99Ms} running={status.state === 'running'} />}
      {!tl && status.state !== 'error' && (
        <p className="max-w-2xl text-[12px] text-muted">
          Steps the design once per second: HPA / VM autoscaler control loops (sync period, tolerance, scale-up policy, scale-down stabilization), pod startup, Pending pods and node provisioning, and
          fluid backlogs when demand exceeds capacity. Retries, circuit breakers and failure injection come in Phase 2.
        </p>
      )}
    </div>
  )
}

function TimelineResult({ tl, sloP99, running }: { tl: Timeline; sloP99?: number; running: boolean }) {
  const s = tl.summary
  const charts = useMemo(() => buildCharts(tl, sloP99), [tl, sloP99])
  return (
    <div className={cx('space-y-4 transition-opacity', running && 'opacity-60')}>
      <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        <Tile label="Peak p99" value={s.peakP99Ms === null ? '–' : fmtMs(s.peakP99Ms)} tone={sloP99 && s.peakP99Ms !== null && s.peakP99Ms > sloP99 ? 'bad' : undefined} />
        <Tile label="Max error rate" value={`${fmt(s.maxErrorRate * 100, 2)}%`} tone={s.maxErrorRate > 0 ? 'bad' : undefined} />
        <Tile label="SLO broken" value={s.sloBreachS ? `${fmt(s.sloBreachS)} s` : 'never'} tone={s.sloBreachS ? 'bad' : 'ok'} />
        <Tile label="First breach" value={s.firstBreachS === null ? '–' : `t = ${fmt(s.firstBreachS)} s`} />
      </div>
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

function Tile({ label, value, tone }: { label: string; value: string; tone?: 'ok' | 'bad' }) {
  return (
    <div className="rounded-lg border border-line bg-panel px-3 py-2">
      <div className="text-[10.5px] font-medium tracking-wide text-muted uppercase">{label}</div>
      <div className={cx('num text-[16px] font-semibold', tone === 'ok' && 'text-ok', tone === 'bad' && 'text-bad')}>{value}</div>
    </div>
  )
}

function buildCharts(tl: Timeline, sloP99?: number): { title: string; option: ChartOption }[] {
  const c = themeColors()
  const t = tl.ticks.map((k) => k.t)
  const grid = { left: 48, right: 48, top: 28, bottom: 22 }
  const xAxis = { type: 'category', data: t, boundaryGap: false, axisLine: { lineStyle: { color: c.line } }, axisLabel: { formatter: '{value}s' } }
  // Units live in the series names: axis names would collide with the legend.
  const yAxis = (extra: object = {}) => ({ type: 'value', splitLine: { lineStyle: { color: c.line } }, ...extra })
  const line = (name: string, data: (number | null)[], extra: object = {}) => ({ name, type: 'line', data, showSymbol: false, lineStyle: { width: 1.5 }, ...extra })
  const legend = { top: 0, left: 40, textStyle: { color: c.muted, fontSize: 10.5 }, itemWidth: 12, itemHeight: 6 }

  // Busiest stations by peak load keep the chart readable.
  const stationPeak = tl.stations.map((id, k) => ({ id, k, peak: Math.max(0, ...tl.ticks.map((x) => x.stations[k].load ?? 10)) }))
  const top = stationPeak.sort((a, b) => b.peak - a.peak).slice(0, MAX_STATION_SERIES)

  return [
    {
      title: 'Load & latency',
      option: {
        grid,
        legend,
        xAxis,
        yAxis: [yAxis(), yAxis({ splitLine: { show: false } })],
        series: [
          line('offered TPS', tl.ticks.map((k) => k.offeredTps), { areaStyle: { opacity: 0.08 } }),
          line('p50 ms', tl.ticks.map((k) => k.latency?.p50Ms ?? null), { yAxisIndex: 1 }),
          line('p99 ms', tl.ticks.map((k) => k.latency?.p99Ms ?? null), {
            yAxisIndex: 1,
            ...(sloP99 ? { markLine: { silent: true, symbol: 'none', data: [{ yAxis: sloP99, name: 'SLO' }], lineStyle: { color: c.bad, type: 'dashed' }, label: { formatter: 'SLO', color: c.bad } } } : {}),
          }),
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
            line(`${svc} ready`, tl.ticks.map((x) => x.replicas[k].ready), { step: 'end' }),
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
            ...(i === 0 ? { markLine: { silent: true, symbol: 'none', data: [{ yAxis: 100 }], lineStyle: { color: c.bad, type: 'dashed' }, label: { show: false } } } : {}),
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
          ...top.map(({ id, k }) => line(`${id} wait`, tl.ticks.map((x) => x.stations[k].waitMs / 1000))),
          line('error rate %', tl.ticks.map((x) => Math.round(x.errorRate * 10000) / 100), { yAxisIndex: 1, lineStyle: { width: 1.5, color: c.bad }, itemStyle: { color: c.bad }, areaStyle: { opacity: 0.1, color: c.bad } }),
        ],
      },
    },
  ]
}
