import { useMemo, useState } from 'react'
import { defaultFormState, ShadowTestForm } from '@/components/ShadowTestForm'
import { YamlPreview } from '@/components/YamlPreview'
import { dumpShadowTestYaml } from '@/lib/shadowTestYaml'

export default function Editor() {
  const [form, setForm] = useState(defaultFormState)
  const yamlText = useMemo(() => dumpShadowTestYaml(form), [form])
  const filename = `${form.name || 'shadowtest'}.yaml`

  return (
    <div className="mx-auto grid h-full max-w-6xl grid-cols-1 gap-4 p-4 lg:grid-cols-2">
      <section className="min-h-0 overflow-y-auto rounded-lg border border-[#1f2937] bg-[#111827] p-4">
        <h1 className="mb-1 text-base font-semibold text-white">ShadowTest Editor</h1>
        <p className="mb-4 text-xs text-slate-500">
          Interactive builder for <code className="text-slate-400">engine.shadow-diff.io/v1alpha1</code>{' '}
          ShadowTest manifests. Copy or download — apply with kubectl separately.
        </p>
        <ShadowTestForm value={form} onChange={setForm} />
      </section>
      <section className="min-h-[320px] lg:min-h-0">
        <YamlPreview yamlText={yamlText} filename={filename} />
      </section>
    </div>
  )
}
