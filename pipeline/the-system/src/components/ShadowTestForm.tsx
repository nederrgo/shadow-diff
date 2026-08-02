import { useState, type ReactNode } from 'react'
import { shadowCatalog } from '@/lib/shadowCatalog'

export type DependencyFormEntry = {
  name: string
  type: string
  envVarInjection: string
  image: string
  port: string
  open: boolean
}

/** Single ingress input. Empty driver means omit spec.inputs (Monarch default). */
export type InputFormEntry = {
  driver: string
  port: string
  prodUrl: string
  exchange: string
  exchangeType: string
  routingKey: string
  targetDependency: string
}

export type ShadowTestFormState = {
  name: string
  namespace: string
  targetDeployment: string
  newImage: string
  mode: 'record' | 'replay'
  s3Bucket: string
  s3Region: string
  s3Secret: string
  retentionPolicy: 'Retain' | 'Delete'
  dependencies: DependencyFormEntry[]
  input: InputFormEntry
}

type Props = {
  value: ShadowTestFormState
  onChange: (next: ShadowTestFormState) => void
}

const fieldClass =
  'w-full rounded border border-[#1f2937] bg-[#090d16] px-2.5 py-1.5 text-sm text-slate-100 outline-none focus:border-[#3b82f6]'

const emptyInput = (): InputFormEntry => ({
  driver: '',
  port: '80',
  prodUrl: '',
  exchange: '',
  exchangeType: 'topic',
  routingKey: '#',
  targetDependency: '',
})

export const defaultFormState: ShadowTestFormState = {
  name: 'my-app-shadow',
  namespace: 'default',
  targetDeployment: 'my-prod-app',
  newImage: 'my-app:candidate',
  mode: 'record',
  s3Bucket: 'shadow-diff-local',
  s3Region: 'us-east-1',
  s3Secret: 'shadow-diff-s3',
  retentionPolicy: 'Retain',
  dependencies: [],
  input: emptyInput(),
}

function newDependency(type: string): DependencyFormEntry {
  const kind = shadowCatalog.dependencies.find((d) => d.type === type)
  return {
    name: type,
    type,
    envVarInjection: kind?.suggestedEnvVar ?? '',
    image: '',
    port: '',
    open: true,
  }
}

export function ShadowTestForm({ value, onChange }: Props) {
  const [addDepType, setAddDepType] = useState(shadowCatalog.dependencies[0]?.type ?? '')

  const set = <K extends keyof ShadowTestFormState>(key: K, v: ShadowTestFormState[K]) => {
    onChange({ ...value, [key]: v })
  }

  const updateDep = (i: number, patch: Partial<DependencyFormEntry>) => {
    const dependencies = value.dependencies.map((d, idx) => (idx === i ? { ...d, ...patch } : d))
    set('dependencies', dependencies)
  }

  const patchInput = (patch: Partial<InputFormEntry>) => {
    set('input', { ...value.input, ...patch })
  }

  const selectDriver = (driver: string) => {
    if (driver === '') {
      set('input', emptyInput())
      return
    }
    patchInput({
      driver,
      port: driver === 'rabbitmq_message' ? '' : value.input.port || '80',
    })
  }

  const depNames = value.dependencies.map((d) => d.name).filter(Boolean)
  const driverMeta = shadowCatalog.inputs.find((d) => d.driver === value.input.driver)

  return (
    <form className="space-y-3" onSubmit={(e) => e.preventDefault()}>
      <Field label="Name">
        <input className={fieldClass} value={value.name} onChange={(e) => set('name', e.target.value)} />
      </Field>
      <Field label="Namespace">
        <input
          className={fieldClass}
          value={value.namespace}
          onChange={(e) => set('namespace', e.target.value)}
        />
      </Field>
      <Field label="Target Deployment">
        <input
          className={fieldClass}
          value={value.targetDeployment}
          onChange={(e) => set('targetDeployment', e.target.value)}
        />
      </Field>
      <Field label="New Image (candidate)">
        <input
          className={fieldClass}
          value={value.newImage}
          onChange={(e) => set('newImage', e.target.value)}
        />
      </Field>
      <Field label="Mode">
        <select
          className={fieldClass}
          value={value.mode}
          onChange={(e) => set('mode', e.target.value as 'record' | 'replay')}
        >
          <option value="record">record</option>
          <option value="replay">replay</option>
        </select>
      </Field>
      <Field label="S3 Bucket">
        <input
          className={fieldClass}
          value={value.s3Bucket}
          onChange={(e) => set('s3Bucket', e.target.value)}
        />
      </Field>
      <Field label="S3 Region">
        <input
          className={fieldClass}
          value={value.s3Region}
          onChange={(e) => set('s3Region', e.target.value)}
        />
      </Field>
      <Field label="S3 Secret">
        <input
          className={fieldClass}
          value={value.s3Secret}
          onChange={(e) => set('s3Secret', e.target.value)}
        />
      </Field>
      <Field label="Retention Policy">
        <select
          className={fieldClass}
          value={value.retentionPolicy}
          onChange={(e) => set('retentionPolicy', e.target.value as 'Retain' | 'Delete')}
        >
          <option value="Retain">Retain</option>
          <option value="Delete">Delete</option>
        </select>
      </Field>

      <section className="space-y-2 border-t border-[#1f2937] pt-4">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-white">Dependencies</h2>
          <p className="text-[11px] text-slate-500">from shadowspec catalog</p>
        </div>
        <div className="flex gap-2">
          <select
            className={fieldClass}
            value={addDepType}
            onChange={(e) => setAddDepType(e.target.value)}
          >
            {shadowCatalog.dependencies.map((d) => (
              <option key={d.type} value={d.type}>
                {d.label}
              </option>
            ))}
          </select>
          <button
            type="button"
            className="shrink-0 rounded border border-[#3b82f6]/40 bg-[#3b82f6]/15 px-3 py-1.5 text-xs font-medium text-[#3b82f6] hover:bg-[#3b82f6]/25"
            onClick={() => {
              if (!addDepType) return
              set('dependencies', [...value.dependencies, newDependency(addDepType)])
            }}
          >
            Add
          </button>
        </div>
        {value.dependencies.length === 0 && (
          <p className="text-xs text-slate-500">No dependencies. Add mongo / rabbitmq / redis / postgres.</p>
        )}
        {value.dependencies.map((dep, i) => {
          const kind = shadowCatalog.dependencies.find((d) => d.type === dep.type)
          return (
            <div key={i} className="rounded border border-[#1f2937] bg-[#090d16]/80">
              <div className="flex items-center gap-2 px-3 py-2">
                <button
                  type="button"
                  className="text-left text-sm font-medium text-slate-200"
                  onClick={() => updateDep(i, { open: !dep.open })}
                >
                  <span className="mr-2 text-slate-500">{dep.open ? '▾' : '▸'}</span>
                  {kind?.label ?? dep.type}
                  <span className="ml-2 font-normal text-slate-500">{dep.name || 'unnamed'}</span>
                </button>
                <button
                  type="button"
                  className="ml-auto text-xs text-red-400 hover:text-red-300"
                  onClick={() => set('dependencies', value.dependencies.filter((_, idx) => idx !== i))}
                >
                  Remove
                </button>
              </div>
              {dep.open && (
                <div className="space-y-2 border-t border-[#1f2937] px-3 py-3">
                  <Field label="Name">
                    <input
                      className={fieldClass}
                      value={dep.name}
                      onChange={(e) => updateDep(i, { name: e.target.value })}
                    />
                  </Field>
                  <Field label="Env var injection">
                    <input
                      className={fieldClass}
                      value={dep.envVarInjection}
                      placeholder={kind?.suggestedEnvVar}
                      onChange={(e) => updateDep(i, { envVarInjection: e.target.value })}
                    />
                  </Field>
                  <Field label={`Image override (default ${kind?.defaultImage ?? '—'})`}>
                    <input
                      className={fieldClass}
                      value={dep.image}
                      placeholder={kind?.defaultImage}
                      onChange={(e) => updateDep(i, { image: e.target.value })}
                    />
                  </Field>
                  <Field label={`Port override (default ${kind?.defaultPort ?? '—'})`}>
                    <input
                      className={fieldClass}
                      value={dep.port}
                      placeholder={String(kind?.defaultPort ?? '')}
                      onChange={(e) => updateDep(i, { port: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            </div>
          )
        })}
      </section>

      <section className="space-y-2 border-t border-[#1f2937] pt-4">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-white">Input</h2>
          <p className="text-[11px] text-slate-500">one driver — from shadowspec</p>
        </div>
        <Field label="Driver">
          <select
            className={fieldClass}
            value={value.input.driver}
            onChange={(e) => selectDriver(e.target.value)}
          >
            <option value="">Default (HTTP on servicePort)</option>
            {shadowCatalog.inputs.map((d) => (
              <option key={d.driver} value={d.driver}>
                {d.label}
              </option>
            ))}
          </select>
        </Field>
        {!value.input.driver && (
          <p className="text-xs text-slate-500">
            Leave as default to omit <code className="text-slate-400">spec.inputs</code>; Monarch binds HTTP on
            servicePort.
          </p>
        )}
        {driverMeta?.needsPort && (
          <Field label="Port">
            <input
              className={fieldClass}
              value={value.input.port}
              onChange={(e) => patchInput({ port: e.target.value })}
            />
          </Field>
        )}
        {driverMeta?.needsAmqp && (
          <div className="space-y-2 rounded border border-[#1f2937] bg-[#090d16]/80 px-3 py-3">
            <Field label="Prod AMQP URL">
              <input
                className={fieldClass}
                value={value.input.prodUrl}
                placeholder="amqp://prod-rabbitmq.default.svc:5672"
                onChange={(e) => patchInput({ prodUrl: e.target.value })}
              />
            </Field>
            <Field label="Exchange">
              <input
                className={fieldClass}
                value={value.input.exchange}
                onChange={(e) => patchInput({ exchange: e.target.value })}
              />
            </Field>
            <Field label="Exchange type">
              <select
                className={fieldClass}
                value={value.input.exchangeType}
                onChange={(e) => patchInput({ exchangeType: e.target.value })}
              >
                <option value="topic">topic</option>
                <option value="direct">direct</option>
                <option value="fanout">fanout</option>
                <option value="headers">headers</option>
              </select>
            </Field>
            <Field label="Routing key">
              <input
                className={fieldClass}
                value={value.input.routingKey}
                onChange={(e) => patchInput({ routingKey: e.target.value })}
              />
            </Field>
            <Field label="Target dependency">
              <select
                className={fieldClass}
                value={value.input.targetDependency}
                onChange={(e) => patchInput({ targetDependency: e.target.value })}
              >
                <option value="">Select rabbitmq dependency…</option>
                {depNames.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        )}
      </section>
    </form>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-xs font-medium text-slate-400">{label}</span>
      {children}
    </label>
  )
}
