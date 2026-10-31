import type { Catalog, Config, Param } from '../engine/types'
import { getPath, setPath } from '../store/model'
import { Chips, Field, NumberInput, Select, SourceBadge, TextInput, Toggle } from '../ui/kit'

/** Values a param accepts, mirroring Catalog.AllowedValues in the engine. */
export function allowedValues(cat: Catalog, p: Param): string[] | undefined {
  switch (p.optionsFrom) {
    case 'instances':
      return Object.keys(cat.instances).sort()
    case 'profiles':
      return Object.keys(cat.profiles).sort()
    case 'protocols':
      return Object.keys(cat.protocols).sort()
  }
  return p.options
}

function show(v: unknown): string {
  if (v === '') return '(none)'
  if (Array.isArray(v)) return v.length ? v.join(', ') : '(none)'
  if (typeof v === 'object' && v !== null) return JSON.stringify(v)
  return String(v)
}

function ResetButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="text-[10.5px] text-muted hover:text-accent" title="Remove your value and use the catalog default">
      reset
    </button>
  )
}

/**
 * One catalog parameter. Unset fields show the catalog default as a
 * placeholder with its source; typing a value overrides it ("yours").
 */
export function ParamField({
  name,
  param,
  config,
  catalog,
  onChange,
}: {
  name: string
  param: Param
  config: Config
  catalog: Catalog
  onChange: (c: Config) => void
}) {
  const value = getPath(config, name)
  const set = value !== undefined
  const commit = (v: unknown) => onChange(setPath(config, name, v))
  const def = param.default
  const label = param.unit ? `${name} (${param.unit})` : name
  const right = (
    <>
      {set && <ResetButton onClick={() => commit(undefined)} />}
      <SourceBadge source={set ? { kind: 'user' } : param.source} />
    </>
  )
  const options = allowedValues(catalog, param)

  if (typeof def === 'number') {
    return (
      <Field label={label} hint={param.description} right={right}>
        {(id) => <NumberInput id={id} value={typeof value === 'number' ? value : undefined} placeholder={show(def)} onCommit={commit} />}
      </Field>
    )
  }
  if (typeof def === 'boolean') {
    const effective = typeof value === 'boolean' ? value : def
    return (
      <Field label={label} hint={param.description} right={right}>
        {(id) => <Toggle id={id} checked={effective} onChange={commit} label={<span className="text-muted">{effective ? 'enabled' : 'disabled'}</span>} />}
      </Field>
    )
  }
  if (Array.isArray(def)) {
    const current = Array.isArray(value) ? (value as string[]) : (def as string[])
    return (
      <Field label={label} hint={param.description} right={right}>
        {() =>
          options ? (
            <Chips options={options} value={current} onChange={commit} />
          ) : (
            <TextInput value={Array.isArray(value) ? value.join(', ') : undefined} placeholder={show(def)} onCommit={(v) => commit(v?.split(',').map((x) => x.trim()).filter(Boolean))} />
          )
        }
      </Field>
    )
  }
  if (typeof def === 'object' && def !== null) {
    // Map of numbers (e.g. opCostMs): the engine merges your entries over the defaults.
    const defaults = def as Record<string, number>
    const mine = (typeof value === 'object' && value !== null ? value : {}) as Record<string, number>
    return (
      <Field label={label} hint={param.description} right={right}>
        {() => (
          <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1">
            {Object.keys(defaults).map((k) => (
              <div key={k} className="contents">
                <span className="font-mono text-[11px] text-muted">{k}</span>
                <NumberInput value={mine[k]} placeholder={show(defaults[k])} min={0} onCommit={(v) => commit(setPath(mine, k, v))} />
              </div>
            ))}
          </div>
        )}
      </Field>
    )
  }
  // String.
  if (options) {
    return (
      <Field label={label} hint={param.description} right={right}>
        {(id) => (
          <Select
            id={id}
            value={typeof value === 'string' ? value : ''}
            options={[{ value: '', label: `default (${show(def)})` }, ...options.map((o) => ({ value: o, label: o }))]}
            onChange={(v) => commit(v === '' ? undefined : v)}
          />
        )}
      </Field>
    )
  }
  return (
    <Field label={label} hint={param.description} right={right}>
      {(id) => <TextInput id={id} value={typeof value === 'string' ? value : undefined} placeholder={show(def)} onCommit={commit} />}
    </Field>
  )
}
