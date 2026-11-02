import { useEffect, useState } from 'react'
import { designKey, useDesign } from '../store/design'
import { server, ServerError, type DesignMeta, type VersionInfo } from '../store/server'
import { templates } from '../store/templates'
import { Button, cx, Dialog, TextInput } from '../ui/kit'

export type DesignsTab = 'saved' | 'history' | 'templates'

/** Saved designs, the current design's version history, and templates. */
export function DesignsDialog({ open, tab, onTab, onClose }: { open: boolean; tab: DesignsTab; onTab: (t: DesignsTab) => void; onClose: () => void }) {
  const linked = useDesign((s) => s.server !== null)
  const tabs: [DesignsTab, string][] = [
    ['saved', 'My designs'],
    ...(linked ? [['history', 'Version history'] as [DesignsTab, string]] : []),
    ['templates', 'Templates'],
  ]
  return (
    <Dialog open={open} onClose={onClose} title="Designs" wide>
      <div className="flex gap-1 border-b border-line px-3 pt-2">
        {tabs.map(([t, label]) => (
          <button
            key={t}
            type="button"
            onClick={() => onTab(t)}
            className={cx('-mb-px border-b-2 px-3 py-1.5 text-[12px] font-medium', tab === t ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg')}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="p-4">
        {tab === 'saved' && <SavedList onOpened={onClose} />}
        {tab === 'history' && linked && <History onOpened={onClose} />}
        {tab === 'templates' && <Templates onOpened={onClose} />}
      </div>
    </Dialog>
  )
}

/** Asks before the current design is replaced, unless it is saved as is. */
function canReplace(): boolean {
  const link = useDesign.getState().server
  if (link && designKey() === link.savedKey) return true
  return confirm(
    link
      ? 'The current design has unsaved changes. Replace it anyway?'
      : 'The current design is not saved to the server. Replace it? (Export it first to keep a copy.)',
  )
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** Runs load when deps change (or on reload()) and tracks its result. */
function useLoad<T>(load: () => Promise<T>, deps: unknown[]) {
  const [state, setState] = useState<{ value?: T; error?: ServerError | Error; loading: boolean }>({ loading: true })
  const [nonce, setNonce] = useState(0)
  useEffect(() => {
    let live = true
    setState((s) => ({ ...s, loading: true }))
    load().then(
      (value) => live && setState({ value, loading: false }),
      (error: Error) => live && setState({ error, loading: false }),
    )
    return () => {
      live = false
    }
  }, [...deps, nonce])
  return { ...state, reload: () => setNonce((n) => n + 1) }
}

function Problem({ error }: { error: Error }) {
  const offline = error instanceof ServerError && !error.available
  return <p className={cx('text-[12px]', offline ? 'text-muted' : 'text-bad')}>{error.message}</p>
}

function SavedList({ onOpened }: { onOpened: () => void }) {
  const current = useDesign((s) => s.server?.id)
  const list = useLoad(() => server.list(), [])
  const [busy, setBusy] = useState<string>()
  const [error, setError] = useState<Error>()

  const open = async (d: DesignMeta) => {
    if (!canReplace()) return
    setBusy(d.id)
    try {
      const v = await server.version(d.id, d.versions)
      useDesign.getState().load({ name: v.name, architecture: v.architecture }, { id: d.id, version: v.version })
      onOpened()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(undefined)
    }
  }
  const remove = async (d: DesignMeta) => {
    if (!confirm(`Delete “${d.name}” and all ${d.versions} version(s)? This cannot be undone.`)) return
    try {
      await server.remove(d.id)
      if (useDesign.getState().server?.id === d.id) useDesign.getState().relink(null)
      list.reload()
    } catch (e) {
      setError(e as Error)
    }
  }

  if (list.loading && !list.value) return <p className="text-[12px] text-muted">Loading…</p>
  if (list.error) return <Problem error={list.error} />
  return (
    <div className="space-y-2">
      {error && <Problem error={error} />}
      {list.value?.length === 0 && <p className="text-[12px] text-muted">Nothing saved yet. Use Save in the top bar to keep the current design and its versions on the server.</p>}
      <ul className="divide-y divide-line">
        {list.value?.map((d) => (
          <li key={d.id} className="flex items-center gap-3 py-2">
            <div className="min-w-0 flex-1">
              <div className="truncate text-[13px] font-medium">
                {d.name} {d.id === current && <span className="ml-1 rounded bg-accent/15 px-1.5 text-[10.5px] text-accent">open</span>}
              </div>
              <div className="text-[11px] text-muted">
                updated {when(d.updatedAt)} · {d.versions} version{d.versions === 1 ? '' : 's'}
              </div>
            </div>
            <Button onClick={() => void open(d)} disabled={busy !== undefined}>
              {busy === d.id ? 'Opening…' : 'Open'}
            </Button>
            <Button variant="danger" onClick={() => void remove(d)}>
              Delete
            </Button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function History({ onOpened }: { onOpened: () => void }) {
  const link = useDesign((s) => s.server)!
  const dirty = useDesign((s) => s.server !== null && designKey() !== s.server.savedKey)
  const history = useLoad(() => server.history(link.id), [link.id, link.version])
  const [note, setNote] = useState('')
  const [error, setError] = useState<Error>()
  const [saving, setSaving] = useState(false)

  const save = async () => {
    setSaving(true)
    try {
      await useDesign.getState().saveToServer(note || undefined)
      setNote('')
      setError(undefined)
    } catch (e) {
      setError(e as Error)
    } finally {
      setSaving(false)
    }
  }
  const fetchVersion = async (v: VersionInfo) => {
    try {
      return await server.version(link.id, v.version)
    } catch (e) {
      setError(e as Error)
      return undefined
    }
  }
  const open = async (v: VersionInfo) => {
    if (!canReplace()) return
    const full = await fetchVersion(v)
    if (!full) return
    // Saving after opening an old version adds a new latest version on top of it.
    useDesign.getState().load({ name: full.name, architecture: full.architecture }, { id: link.id, version: v.version })
    onOpened()
  }
  const compare = async (v: VersionInfo) => {
    const full = await fetchVersion(v)
    if (!full) return
    useDesign.getState().pinBaseline({ name: `${full.name} (v${v.version})`, architecture: full.architecture })
    onOpened()
  }

  return (
    <div className="space-y-3">
      <div className="flex items-end gap-2">
        <div className="flex-1">
          <label htmlFor="version-note" className="mb-1 block text-[11px] text-muted">
            Note for a new version (optional)
          </label>
          <TextInput id="version-note" value={note} placeholder="e.g. added a read replica" onCommit={(v) => setNote(v ?? '')} />
        </div>
        <Button variant="primary" onClick={() => void save()} disabled={saving || !dirty} title={dirty ? undefined : 'No changes since the last saved version'}>
          {saving ? 'Saving…' : 'Save version'}
        </Button>
      </div>
      {error && <Problem error={error} />}
      {history.error && <Problem error={history.error} />}
      {history.loading && !history.value && <p className="text-[12px] text-muted">Loading…</p>}
      <ul className="divide-y divide-line">
        {history.value?.history.map((v) => (
          <li key={v.version} className="flex items-center gap-3 py-2">
            <span className="num w-10 text-[12px] font-semibold">v{v.version}</span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[12.5px]">
                {v.note || <span className="text-muted">{v.name}</span>}
                {v.version === link.version && <span className="ml-2 rounded bg-accent/15 px-1.5 text-[10.5px] text-accent">{dirty ? 'base of your edits' : 'open'}</span>}
              </div>
              <div className="text-[11px] text-muted">{when(v.savedAt)}</div>
            </div>
            <Button variant="ghost" title="Pin this version as the comparison baseline" onClick={() => void compare(v)}>
              Compare
            </Button>
            <Button onClick={() => void open(v)}>Open</Button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function Templates({ onOpened }: { onOpened: () => void }) {
  const use = (t: (typeof templates)[number]) => {
    if (!canReplace()) return
    useDesign.getState().load(t.design())
    history.replaceState(null, '', location.pathname)
    onOpened()
  }
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {templates.map((t) => (
        <button key={t.id} type="button" onClick={() => use(t)} className="rounded-lg border border-line p-3 text-left hover:border-accent hover:bg-panel-2">
          <div className="text-[13px] font-medium">{t.title}</div>
          <div className="mt-1 text-[11.5px] text-muted">{t.description}</div>
          <div className="mt-2 text-[10.5px] text-faint">{t.nodes} components</div>
        </button>
      ))}
    </div>
  )
}
