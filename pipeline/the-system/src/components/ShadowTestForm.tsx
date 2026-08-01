import type { ReactNode } from 'react'

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
}

type Props = {
  value: ShadowTestFormState
  onChange: (next: ShadowTestFormState) => void
}

const fieldClass =
  'w-full rounded border border-[#1f2937] bg-[#090d16] px-2.5 py-1.5 text-sm text-slate-100 outline-none focus:border-[#3b82f6]'

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
}

export function ShadowTestForm({ value, onChange }: Props) {
  const set = <K extends keyof ShadowTestFormState>(key: K, v: ShadowTestFormState[K]) => {
    onChange({ ...value, [key]: v })
  }

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
