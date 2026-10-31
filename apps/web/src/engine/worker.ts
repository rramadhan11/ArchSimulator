// Web Worker that hosts the Go→WASM engine (PLAN v1.1 #11). Engine calls
// are synchronous and CPU-bound, so they run here instead of the UI thread.
import './wasm_exec.js'

interface EngineAPI {
  catalog(): string
  run(design: string): string
  simulate(design: string): string
}

declare global {
  // Defined by wasm_exec.js and by the Go program respectively.
  // eslint-disable-next-line no-var
  var Go: new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> }
  // eslint-disable-next-line no-var
  var archsim: EngineAPI | undefined
}

export type Request = { id: number; op: 'catalog' } | { id: number; op: 'run' | 'simulate'; design: string }
// ms is the engine call alone (no queueing, no WASM start-up).
export type Response = { id: number; result?: unknown; error?: string; ms?: number }

const ready: Promise<EngineAPI> = (async () => {
  const go = new Go()
  const wasmURL = new URL(`${import.meta.env.BASE_URL}engine.wasm`, self.location.origin)
  const { instance } = await WebAssembly.instantiateStreaming(fetch(wasmURL), go.importObject)
  void go.run(instance) // resolves only when the Go program exits, which it never does
  if (!globalThis.archsim) throw new Error('engine did not register itself')
  return globalThis.archsim
})()

self.onmessage = async (ev: MessageEvent<Request>) => {
  const req = ev.data
  let res: Response
  try {
    const api = await ready
    const t0 = performance.now()
    const raw = req.op === 'catalog' ? api.catalog() : api[req.op](req.design)
    res = { id: req.id, ms: performance.now() - t0, ...(JSON.parse(raw) as { result?: unknown; error?: string }) }
  } catch (err) {
    res = { id: req.id, error: `engine failed to load: ${err instanceof Error ? err.message : String(err)}` }
  }
  self.postMessage(res)
}
