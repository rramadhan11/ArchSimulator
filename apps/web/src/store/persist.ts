// Local-first persistence (PLAN v1.1 #9): autosave to localStorage, file
// export/import, and share links that carry the whole design in the URL hash.
import type { Architecture } from '../engine/types'

export interface SavedDesign {
  name: string
  architecture: Architecture
}

const STORAGE_KEY = 'archsim:design:v1'
const HASH_PREFIX = '#d='
// Share links are untrusted input: cap the inflated size so a crafted link
// cannot exhaust memory (a design is a few KB; 2 MB is generous).
const MAX_DESIGN_BYTES = 2 << 20

export function saveLocal(d: SavedDesign): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(d))
  } catch {
    // Storage can be full or disabled (private mode); the design stays in memory.
  }
}

export function loadLocal(): SavedDesign | undefined {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? parseSaved(raw) : undefined
  } catch {
    return undefined
  }
}

/** Checks the shape the editor relies on; the engine validates the rest. */
export function parseSaved(raw: string): SavedDesign {
  const v: unknown = JSON.parse(raw)
  const obj = v as Partial<SavedDesign> & Partial<Architecture>
  // Accept both {name, architecture} and a bare architecture file.
  const a = (obj.architecture ?? obj) as Partial<Architecture>
  if (!a || typeof a !== 'object' || !Array.isArray(a.nodes) || !Array.isArray(a.edges) || typeof a.workload !== 'object' || a.workload === null) {
    throw new Error('not an ArchSim design: expected nodes, edges and workload')
  }
  for (const n of a.nodes) {
    if (typeof n?.id !== 'string' || typeof n?.kind !== 'string') throw new Error('every node needs a string id and kind')
  }
  for (const e of a.edges) {
    if (typeof e?.from !== 'string' || typeof e?.to !== 'string') throw new Error('every edge needs string from and to')
  }
  const name = typeof obj.name === 'string' && obj.architecture ? obj.name : 'Imported design'
  return { name, architecture: a as Architecture }
}

function toBase64Url(bytes: Uint8Array): string {
  let bin = ''
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function fromBase64Url(s: string): Uint8Array {
  const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

async function readCapped(stream: ReadableStream<Uint8Array>, limit: number): Promise<Uint8Array> {
  const reader = stream.getReader()
  const chunks: Uint8Array[] = []
  let size = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    size += value.length
    if (size > limit) {
      await reader.cancel()
      throw new Error('shared design is too large')
    }
    chunks.push(value)
  }
  const out = new Uint8Array(size)
  let off = 0
  for (const c of chunks) {
    out.set(c, off)
    off += c.length
  }
  return out
}

export async function encodeShare(d: SavedDesign): Promise<string> {
  const data = new TextEncoder().encode(JSON.stringify(d))
  const stream = new Blob([data]).stream().pipeThrough(new CompressionStream('deflate-raw'))
  return HASH_PREFIX + toBase64Url(await readCapped(stream, Infinity))
}

/** Returns the design in a share-link hash, or undefined when there is none. */
export async function decodeShare(hash: string): Promise<SavedDesign | undefined> {
  if (!hash.startsWith(HASH_PREFIX)) return undefined
  const bytes = fromBase64Url(hash.slice(HASH_PREFIX.length))
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('deflate-raw'))
  const json = new TextDecoder().decode(await readCapped(stream, MAX_DESIGN_BYTES))
  return parseSaved(json)
}
