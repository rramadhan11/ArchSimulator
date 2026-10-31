import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { engine } from './engine/client'
import './index.css'
import { startAutoEffects, useDesign } from './store/design'
import { decodeShare, loadLocal, type SavedDesign } from './store/persist'
import { templates } from './store/templates'

const root = createRoot(document.getElementById('root')!)

async function initialDesign(): Promise<{ design: SavedDesign; warning?: string }> {
  try {
    const shared = await decodeShare(location.hash)
    if (shared) return { design: shared }
  } catch (e) {
    const fallback = loadLocal() ?? templates[0].design()
    return { design: fallback, warning: `The share link could not be opened: ${e instanceof Error ? e.message : String(e)}` }
  }
  const local = loadLocal()
  if (local) return { design: local }
  return { design: (templates.find((t) => t.id === 'k8s-kong-microservices') ?? templates[0]).design() }
}

async function boot() {
  root.render(<p style={{ padding: 24, color: 'var(--muted)' }}>Loading engine…</p>)
  try {
    const catalog = await engine.catalog()
    useDesign.setState({ catalog })
    const { design, warning } = await initialDesign()
    useDesign.getState().load(design)
    startAutoEffects()
    void useDesign.getState().runCheck()
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    if (warning) setTimeout(() => alert(warning), 0)
  } catch (e) {
    root.render(
      <div style={{ padding: 24, fontFamily: 'ui-monospace, monospace' }}>
        <b>ArchSim could not start.</b>
        <pre style={{ whiteSpace: 'pre-wrap' }}>{e instanceof Error ? e.message : String(e)}</pre>
        <p>Is engine.wasm built? Run <code>make wasm</code> from the repository root.</p>
      </div>,
    )
  }
}

void boot()
