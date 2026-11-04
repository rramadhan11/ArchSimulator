import { useState } from 'react'
import type { Cost } from '../engine/types'
import { latest, useDesign } from '../store/design'
import { Button, cx, Dialog, fmt, NumberInput, SourceBadge } from '../ui/kit'

export const usd = (n: number, digits = 0) => `$${fmt(n, digits)}`

/** Steady-state cost at the target TPS, with the rates it used. */
export function CostView({ cost }: { cost: Cost }) {
  const [editing, setEditing] = useState(false)
  return (
    <div>
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-[11px] font-semibold tracking-wide text-muted uppercase">Cost (on-demand)</h3>
        <span className="num text-[13px] font-semibold">{usd(cost.monthlyUSD)} / month</span>
        {cost.unpriced?.length ? <span className="text-[11.5px] text-warn">+ {cost.unpriced.length} rate(s) not set</span> : null}
        <Button variant="ghost" className="ml-auto" onClick={() => setEditing(true)}>
          Edit rates
        </Button>
      </div>
      <table className="w-full text-[12px]">
        <tbody>
          {cost.items.map((it) => (
            <tr key={it.node} className="border-t border-line">
              <td className="py-1 pr-2 font-mono text-[11.5px]">{it.node}</td>
              <td className="num py-1 pr-2 text-muted">
                {it.count} × {it.rate}
              </td>
              <td className="num py-1 pr-2 text-right text-muted">
                {it.hourlyUSD === null ? <span className="text-warn">no rate</span> : `${usd(it.hourlyUSD, 4)}/h`}
                {it.source === 'yours' && <span className="ml-1 rounded bg-accent/15 px-1 text-[10px] text-accent">yours</span>}
              </td>
              <td className="num w-24 py-1 text-right">{it.monthlyUSD === null ? '–' : usd(it.monthlyUSD)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-1.5 text-[11px] text-muted">
        Instances only (730 h a month): load balancers, queues, caches, external APIs, storage and traffic are not included.
      </p>
      <RatesDialog open={editing} onClose={() => setEditing(false)} />
    </div>
  )
}

/** The rates the design uses: the catalog's current one and the design's own. */
export function RatesDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const catalog = useDesign((s) => s.catalog)
  const own = useDesign((s) => s.pricing?.hourlyUSD)
  const cost = useDesign((s) => latest(s.check)?.target.cost) // select stable objects, derive below
  const keys = [...new Set([...(cost?.items.map((i) => i.rate) ?? []), ...Object.keys(own ?? {})])].sort()
  const { setRate } = useDesign.getState()
  return (
    <Dialog open={open} onClose={onClose} title="Rates (USD per instance hour)">
      <div className="space-y-3 p-4">
        <p className="flex items-start gap-2 text-[12px] text-muted">
          <SourceBadge source={catalog.pricesSource} />
          <span>
            Current rates: {catalog.pricesSource.note}. Enter your own (a discount, another region or cloud, your VPS); leave empty for the current rate. Saved with the design.
          </span>
        </p>
        {keys.length === 0 && <p className="text-[12px] text-muted">The design has nothing billed per instance hour yet (node pools, VM groups, databases).</p>}
        <table className="w-full text-[12px]">
          <tbody>
            {keys.map((key) => {
              const current = catalog.hourlyUSD[key]
              return (
                <tr key={key} className="border-t border-line">
                  <td className="py-1.5 pr-2 font-mono text-[11.5px]">{key}</td>
                  <td className={cx('num py-1.5 pr-2 text-right', current === undefined && 'text-warn')}>
                    {current === undefined ? 'no current rate' : `${usd(current, 4)} current`}
                  </td>
                  <td className="w-32 py-1.5">
                    <NumberInput
                      value={own?.[key]}
                      min={0}
                      step={0.0001}
                      placeholder={current === undefined ? 'set a rate' : String(current)}
                      onCommit={(v) => setRate(key, v)}
                    />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </Dialog>
  )
}
