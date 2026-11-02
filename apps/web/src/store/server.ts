// Client for the server's design storage (apps/api/designs.go).
import type { Architecture } from '../engine/types'
import type { SavedDesign } from './persist'

export interface DesignMeta {
  id: string
  name: string
  createdAt: string
  updatedAt: string
  versions: number
}

export interface VersionInfo {
  version: number
  savedAt: string
  note?: string
  name: string
}

export class ServerError extends Error {
  constructor(
    message: string,
    /** False when there is no storage API at all (e.g. the Vite dev server). */
    readonly available = true,
  ) {
    super(message)
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    })
  } catch {
    throw new ServerError('The server cannot be reached.')
  }
  if (res.status === 401) throw new ServerError('Your session has expired: reload the page to sign in again.')
  if (res.status === 204) return undefined as T
  const isJSON = res.headers.get('Content-Type')?.startsWith('application/json')
  if (!isJSON) throw new ServerError('Saving to the server needs the ArchSim server (not available in dev mode).', false)
  const data = (await res.json()) as T & { error?: string }
  if (!res.ok) throw new ServerError(data.error ?? `Server error ${res.status}`)
  return data
}

export const server = {
  list: () => call<DesignMeta[]>('GET', '/api/designs'),
  history: (id: string) => call<DesignMeta & { history: VersionInfo[] }>('GET', `/api/designs/${encodeURIComponent(id)}`),
  create: (d: SavedDesign, note?: string) => call<DesignMeta>('POST', '/api/designs', { name: d.name, note, architecture: d.architecture }),
  save: (id: string, d: SavedDesign, note?: string) =>
    call<DesignMeta>('POST', `/api/designs/${encodeURIComponent(id)}/versions`, { name: d.name, note, architecture: d.architecture }),
  version: (id: string, n: number) =>
    call<VersionInfo & { architecture: Architecture }>('GET', `/api/designs/${encodeURIComponent(id)}/versions/${n}`),
  remove: (id: string) => call<void>('DELETE', `/api/designs/${encodeURIComponent(id)}`),
}
