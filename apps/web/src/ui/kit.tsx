// Small UI primitives shared by the panels.
import { useEffect, useId, useState, type ButtonHTMLAttributes, type ReactNode } from 'react'
import type { Source, SourceKind, Verdict } from '../engine/types'

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'ghost' | 'danger' | 'default' }

export function Button({ variant = 'default', className, ...rest }: ButtonProps) {
  return (
    <button
      type="button"
      className={cx(
        'inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-[12px] font-medium whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50',
        variant === 'primary' && 'bg-accent text-white hover:brightness-110',
        variant === 'default' && 'border border-line bg-panel text-fg hover:bg-panel-2',
        variant === 'ghost' && 'text-muted hover:bg-panel-2 hover:text-fg',
        variant === 'danger' && 'border border-line bg-panel text-bad hover:bg-bad-soft',
        className,
      )}
      {...rest}
    />
  )
}

export function Section({ title, children, defaultOpen = true, right }: { title: string; children: ReactNode; defaultOpen?: boolean; right?: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <section className="border-b border-line">
      <div className="flex items-center gap-2 px-4 py-2.5">
        <button type="button" onClick={() => setOpen(!open)} className="flex flex-1 items-center gap-1.5 text-left text-[11px] font-semibold tracking-wide text-muted uppercase">
          <span className={cx('inline-block w-2 transition-transform', open && 'rotate-90')}>›</span>
          {title}
        </button>
        {right}
      </div>
      {open && <div className="space-y-3 px-4 pb-4">{children}</div>}
    </section>
  )
}

export function Field({ label, hint, children, right }: { label: ReactNode; hint?: ReactNode; children: (id: string) => ReactNode; right?: ReactNode }) {
  const id = useId()
  return (
    <div>
      <div className="mb-1 flex items-center gap-2">
        <label htmlFor={id} className="flex-1 truncate font-mono text-[11.5px] text-fg">
          {label}
        </label>
        {right}
      </div>
      {children(id)}
      {hint && <p className="mt-1 text-[11px] leading-snug text-muted">{hint}</p>}
    </div>
  )
}

const inputCls =
  'h-7 w-full rounded-md border border-line bg-panel px-2 text-[12px] text-fg outline-none placeholder:text-faint focus:border-accent focus:ring-2 focus:ring-accent/20'

/**
 * Number input that keeps the typed text while editing and commits on blur
 * or Enter. An empty field commits undefined ("use the default").
 */
export function NumberInput({
  id,
  value,
  onCommit,
  placeholder,
  min,
  max,
  step,
  className,
}: {
  id?: string
  value: number | undefined
  onCommit: (v: number | undefined) => void
  placeholder?: string
  min?: number
  max?: number
  step?: number
  className?: string
}) {
  const [text, setText] = useState(value === undefined ? '' : String(value))
  const [bad, setBad] = useState(false)
  useEffect(() => setText(value === undefined ? '' : String(value)), [value])
  const commit = () => {
    if (text.trim() === '') {
      setBad(false)
      if (value !== undefined) onCommit(undefined)
      return
    }
    const n = Number(text)
    const ok = Number.isFinite(n) && (min === undefined || n >= min) && (max === undefined || n <= max)
    setBad(!ok)
    if (ok && n !== value) onCommit(n)
  }
  return (
    <input
      id={id}
      inputMode="decimal"
      className={cx(inputCls, 'num', bad && 'border-bad focus:border-bad', className)}
      value={text}
      placeholder={placeholder}
      step={step}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
      aria-invalid={bad}
      title={bad ? `Enter a number${min !== undefined ? ` ≥ ${min}` : ''}${max !== undefined ? ` ≤ ${max}` : ''}` : undefined}
    />
  )
}

export function TextInput({
  id,
  value,
  onCommit,
  placeholder,
  className,
}: {
  id?: string
  value: string | undefined
  onCommit: (v: string | undefined) => void
  placeholder?: string
  className?: string
}) {
  const [text, setText] = useState(value ?? '')
  useEffect(() => setText(value ?? ''), [value])
  const commit = () => {
    const v = text.trim() === '' ? undefined : text.trim()
    if (v !== value) onCommit(v)
  }
  return (
    <input
      id={id}
      className={cx(inputCls, className)}
      value={text}
      placeholder={placeholder}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
    />
  )
}

export function Select<T extends string>({
  id,
  value,
  options,
  onChange,
  className,
}: {
  id?: string
  value: T
  options: { value: T; label: string }[]
  onChange: (v: T) => void
  className?: string
}) {
  return (
    <select id={id} className={cx(inputCls, 'pr-6', className)} value={value} onChange={(e) => onChange(e.target.value as T)}>
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  )
}

export function Toggle({ id, checked, onChange, label }: { id?: string; checked: boolean; onChange: (v: boolean) => void; label?: ReactNode }) {
  return (
    // relative: the visually hidden checkbox is absolutely positioned; without a
    // positioned ancestor it escapes the scrolling panel and makes the page scroll.
    <label className="relative inline-flex cursor-pointer items-center gap-2 text-[12px]">
      <input id={id} type="checkbox" className="peer sr-only" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className="relative h-4 w-7 rounded-full bg-line-strong transition-colors peer-checked:bg-accent peer-focus-visible:ring-2 peer-focus-visible:ring-accent/30 after:absolute after:top-0.5 after:left-0.5 after:h-3 after:w-3 after:rounded-full after:bg-white after:transition-transform peer-checked:after:translate-x-3" />
      {label}
    </label>
  )
}

export function Chips({ options, value, onChange }: { options: string[]; value: string[]; onChange: (v: string[]) => void }) {
  return (
    <div className="flex flex-wrap gap-1">
      {options.map((o) => {
        const on = value.includes(o)
        return (
          <button
            key={o}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(on ? value.filter((x) => x !== o) : [...value, o])}
            className={cx(
              'h-6 rounded-full border px-2 font-mono text-[11px] transition-colors',
              on ? 'border-accent bg-accent-soft text-accent' : 'border-line text-muted hover:text-fg',
            )}
          >
            {o}
          </button>
        )
      })}
    </div>
  )
}

const sourceStyle: Record<SourceKind, { label: string; cls: string; title: string }> = {
  user: { label: 'yours', cls: 'bg-accent-soft text-accent', title: 'Set by you' },
  'vendor-docs': { label: 'docs', cls: 'bg-ok-soft text-ok', title: 'Documented product default or spec' },
  'vendor-benchmark': { label: 'benchmark', cls: 'bg-warn-soft text-warn', title: 'Vendor benchmark, ideal conditions' },
  'default-estimate': { label: 'estimate', cls: 'bg-panel-2 text-muted border border-line', title: 'Rough catalog estimate: replace with your measurement' },
}

export function SourceBadge({ source }: { source: Source }) {
  const s = sourceStyle[source.kind]
  const title = [s.title, source.note].filter(Boolean).join(' — ')
  const badge = <span className={cx('rounded px-1.5 py-px text-[10px] font-medium', s.cls)}>{s.label}</span>
  return source.url ? (
    <a href={source.url} target="_blank" rel="noreferrer noopener" title={`${title}\n${source.url}`} className="hover:opacity-80">
      {badge}
    </a>
  ) : (
    <span title={title}>{badge}</span>
  )
}

export const verdictStyle: Record<Verdict, { label: string; cls: string; dot: string }> = {
  safe: { label: 'Safe', cls: 'bg-ok-soft text-ok', dot: 'bg-ok' },
  warning: { label: 'Warning', cls: 'bg-warn-soft text-warn', dot: 'bg-warn' },
  overload: { label: 'Overload', cls: 'bg-bad-soft text-bad', dot: 'bg-bad' },
}

/** Heat color for a utilization against the workload thresholds. */
export function heat(u: number | null, warn: number, overload: number): 'ok' | 'warn' | 'bad' {
  if (u === null || u >= overload) return 'bad'
  return u >= warn ? 'warn' : 'ok'
}

export function UtilBar({ u, warn, overload }: { u: number | null; warn: number; overload: number }) {
  const h = heat(u, warn, overload)
  const pct = u === null ? 100 : Math.min(100, u * 100)
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-panel-2">
        <div className={cx('h-full rounded-full', h === 'ok' && 'bg-ok', h === 'warn' && 'bg-warn', h === 'bad' && 'bg-bad')} style={{ width: `${pct}%` }} />
      </div>
      <span className="num w-11 shrink-0 text-right text-[11px]">{u === null ? 'n/a' : `${Math.round(u * 100)}%`}</span>
    </div>
  )
}

export function fmt(n: number, digits = 0): string {
  if (!Number.isFinite(n)) return '–'
  return n.toLocaleString('en-US', { maximumFractionDigits: digits, minimumFractionDigits: 0 })
}

export function fmtMs(ms: number): string {
  if (ms >= 10000) return `${fmt(ms / 1000, 1)} s`
  return `${fmt(ms, ms < 10 ? 2 : ms < 100 ? 1 : 0)} ms`
}
