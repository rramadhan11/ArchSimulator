import { describe, expect, it } from 'vitest'
import type { Architecture } from '../engine/types'
import { buildReport } from './report'

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
})
