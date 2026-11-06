import { Panel } from '@xyflow/react'
import type { Timeline } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { PLAY_SPEEDS, usePlayback } from '../store/playback'
import { Button, fmt, fmtMs, Select } from '../ui/kit'

const speedLabel = (s: number) => (s === 0 ? 'real time' : `in ${s} s`)

/** How long an event stays on the bar after it happens, in simulated seconds. */
const EVENT_S = 15

/**
 * Plays the latest time simulation back on the canvas: node load, calls per
 * edge, and pods (Deployments, VM groups) and nodes (node pools) per second.
 */
export function PlaybackBar() {
  const status = useDesign((s) => s.timeline)
  const stale = useDesign((s) => s.timelineStale)
  const tl = latest(status)
  if (!tl || !tl.ticks.length) return null
  return (
    <Panel position="top-center" className="!m-2 flex max-w-[calc(100%-1rem)] items-center gap-2 rounded-lg border border-line bg-panel px-2 py-1.5 text-[12px] shadow-sm">
      {stale || status.state === 'running' ? (
        <span className="px-1 text-muted">
          {status.state === 'running' ? 'Simulating…' : 'The design changed since the last simulation: run it again to play it back.'}
        </span>
      ) : (
        <Controls tl={tl} />
      )}
    </Panel>
  )
}

function Controls({ tl }: { tl: Timeline }) {
  const playing = usePlayback((s) => s.tl === tl && s.playing)
  const active = usePlayback((s) => s.tl === tl)
  const tick = usePlayback((s) => (s.tl === tl ? s.tick : 0))
  const speed = usePlayback((s) => s.speed)
  const { play, pause, stop, seek, setSpeed } = usePlayback.getState()
  const k = tl.ticks[tick]
  const t = k.t
  const event = active ? tl.events?.filter((e) => e.t <= t && e.t > t - EVENT_S).at(-1) : undefined

  return (
    <>
      <Button variant="primary" className="w-[68px] justify-center" onClick={() => (playing ? pause() : play(tl))} title="Play the time simulation back on the canvas">
        {playing ? '❚❚ Pause' : '▶ Play'}
      </Button>
      <Button variant="ghost" onClick={stop} disabled={!active} title="Back to the quick check on the canvas">
        ■
      </Button>
      <input
        type="range"
        aria-label="Simulated time"
        className="w-40 accent-[var(--accent)] sm:w-56"
        min={0}
        max={tl.ticks.length - 1}
        value={tick}
        onChange={(e) => seek(tl, Number(e.target.value))}
      />
      <span className="num min-w-[104px] shrink-0 whitespace-nowrap text-muted">
        t = {fmt(t)} / {fmt(tl.ticks.length * tl.tickS)} s
      </span>
      {active && (
        <span className="num hidden shrink-0 whitespace-nowrap md:inline">
          {fmt(k.offeredTps)} TPS · p99 {k.latency ? fmtMs(k.latency.p99Ms) : '–'} ·{' '}
          <span className={k.errorRate > 0 ? 'text-bad' : undefined}>{fmt(k.errorRate * 100, k.errorRate < 0.01 ? 2 : 1)}% errors</span>
        </span>
      )}
      <Select
        className="!h-7 !w-auto shrink-0"
        value={String(speed)}
        options={PLAY_SPEEDS.map((s) => ({ value: String(s), label: speedLabel(s) }))}
        onChange={(v) => setSpeed(Number(v) as (typeof PLAY_SPEEDS)[number])}
      />
      {event && (
        <span className="hidden max-w-[260px] truncate text-muted lg:inline" title={`t=${event.t}s ${event.ref}: ${event.message}`}>
          <span className="font-mono text-[11px] text-fg">{event.ref}</span> {event.message}
        </span>
      )}
    </>
  )
}
