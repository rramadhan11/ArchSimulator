import { BaseEdge, getBezierPath, type EdgeProps } from '@xyflow/react'
import { memo, useMemo } from 'react'
import type { FlowEdge } from '../store/model'
import { latest, useDesign } from '../store/design'
import { fmt, heat } from '../ui/kit'
import { useInsight, useLimits } from './nodes'

/** Calls per second per edge from the latest quick check. */
function useCallRate(id: string): number | undefined {
  const check = useDesign((s) => s.check)
  const rates = useMemo(() => new Map(latest(check)?.target.edges?.map((e) => [e.edge, e.callsPerS])), [check])
  return rates.get(id)
}

const dotFill = { ok: 'var(--ok)', warn: 'var(--warn)', bad: 'var(--bad)' }

/**
 * An edge with dots flowing along it: more and faster dots for more calls per
 * second (log scale), colored by how loaded the callee is. The duration does
 * not depend on the edge's length, so dragging a node does not restart the
 * animation.
 */
export const TrafficEdge = memo(function TrafficEdge(props: EdgeProps<FlowEdge>) {
  const { id, target, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition } = props
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition })
  const rate = useCallRate(id)
  const callee = useInsight(target)
  const { warn, overload } = useLimits()

  const decades = rate && rate > 0 ? Math.log10(rate) : undefined
  const dots = decades === undefined ? 0 : Math.min(5, Math.max(1, 1 + Math.floor(decades)))
  const durS = decades === undefined ? 0 : Math.min(2.4, Math.max(0.6, 2.4 - 0.4 * decades))
  const fill = callee?.util !== undefined ? dotFill[heat(callee.util, warn, overload)] : 'var(--accent)'
  const label = rate === undefined ? props.label : [props.label, `${fmt(rate, rate < 10 ? 1 : 0)}/s`].filter(Boolean).join(' · ')

  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        labelX={labelX}
        labelY={labelY}
        label={label}
        markerEnd={props.markerEnd}
        style={props.style}
        interactionWidth={props.interactionWidth}
      />
      {dots > 0 && (
        <g key={`${dots}-${durS.toFixed(1)}`} className="traffic" aria-hidden>
          {Array.from({ length: dots }, (_, i) => (
            <circle key={i} r={2.6} fill={fill}>
              <animateMotion dur={`${durS}s`} begin={`${(-i * durS) / dots}s`} repeatCount="indefinite" path={path} />
            </circle>
          ))}
        </g>
      )}
    </>
  )
})

export const edgeTypes = { traffic: TrafficEdge }
