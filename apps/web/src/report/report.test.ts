import { describe, expect, it } from 'vitest'
import type { Architecture } from '../engine/types'
import { buildReport, costSection } from './report'

const hostile = '<img src=x onerror=alert(1)>'

const architecture: Architecture = {
  version: '0.1',
  nodes: [
    { id: 'users', kind: 'client.web' },
    { id: 'api', kind: 'service.go', name: hostile },
  ],
  edges: [{ from: 'users', to: 'api', timeoutMs: 800, retry: { max: 2, backoffMs: 50, budgetPercent: 10 } }],
  workload: { entry: 'users', targetTps: 100 },
}

describe('report', () => {
  it('escapes everything that comes from the design', () => {
    const html = buildReport({ name: hostile, architecture, generatedAt: new Date(0) })
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;')
    expect(html).not.toMatch(/<script/i)
  })

  it('lists the design and says which results are missing', () => {
    const html = buildReport({ name: 'Shop', architecture, generatedAt: new Date(0) })
    expect(html).toContain('<title>Shop — ArchSim report</title>')
    expect(html).toContain('users → api')
    expect(html).toContain('2× / 50 ms / budget 10%')
    expect(html).toContain('No quick check result.')
    expect(html).toContain('Not run for this report.')
  })

  it('prices what has a rate and lists what has none', () => {
    const html = costSection({
      monthlyUSD: 197.1,
      items: [
        { node: 'pg', rate: 'db.r6g.large/postgres', count: 1, hourlyUSD: 0.27, monthlyUSD: 197.1, source: 'catalog' },
        { node: hostile, rate: 'generic-2c4g', count: 2, hourlyUSD: null, monthlyUSD: null },
      ],
      unpriced: ['generic-2c4g'],
    })
    expect(html).toContain('$0.27/h')
    expect(html).toContain('<b>$197</b> + generic-2c4g (no rate)')
    expect(html).toContain('no rate set')
    expect(html).not.toContain('<img')
  })
})
