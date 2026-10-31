import type { Architecture, Catalog, Result, Timeline } from './types'
import type { Request, Response } from './worker'

type Pending = { resolve: (v: Timed<unknown>) => void; reject: (e: Error) => void }

/** An engine result with the time the engine call itself took. */
export interface Timed<T> {
  value: T
  ms: number
}

let worker: Worker | undefined
let nextId = 1
const pending = new Map<number, Pending>()

function getWorker(): Worker {
  if (!worker) {
    worker = new Worker(new URL('./worker.ts', import.meta.url), { type: 'module' })
    worker.onmessage = (ev: MessageEvent<Response>) => {
      const p = pending.get(ev.data.id)
      if (!p) return
      pending.delete(ev.data.id)
      if (ev.data.error !== undefined) p.reject(new EngineError(ev.data.error))
      else p.resolve({ value: ev.data.result, ms: ev.data.ms ?? 0 })
    }
    worker.onerror = (ev) => {
      for (const p of pending.values()) p.reject(new Error(ev.message || 'engine worker crashed'))
      pending.clear()
    }
  }
  return worker
}

/** An error reported by the engine itself (invalid design, unknown kind, ...). */
export class EngineError extends Error {}

type WithoutId<T> = T extends unknown ? Omit<T, 'id'> : never

function call<T>(req: WithoutId<Request>): Promise<Timed<T>> {
  const id = nextId++
  return new Promise<Timed<T>>((resolve, reject) => {
    pending.set(id, { resolve: resolve as (v: Timed<unknown>) => void, reject })
    getWorker().postMessage({ ...req, id })
  })
}

export const engine = {
  catalog: () => call<Catalog>({ op: 'catalog' }).then((r) => r.value),
  run: (a: Architecture) => call<Result>({ op: 'run', design: JSON.stringify(a) }),
  simulate: (a: Architecture) => call<Timeline>({ op: 'simulate', design: JSON.stringify(a) }),
}
