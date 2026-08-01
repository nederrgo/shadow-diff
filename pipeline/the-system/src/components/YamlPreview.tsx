import { useMemo, useState, type ReactNode } from 'react'
import { Check, Copy, Download } from 'lucide-react'

type Props = {
  yamlText: string
  filename: string
}

/** Lightweight YAML highlighter — keys, strings, and comments only. */
function highlightYaml(src: string): ReactNode[] {
  return src.split('\n').map((line, i) => {
    const comment = line.match(/^(\s*)(#.*)$/)
    if (comment) {
      return (
        <div key={i}>
          <span className="text-slate-500">{comment[1]}</span>
          <span className="text-slate-500">{comment[2]}</span>
        </div>
      )
    }
    const kv = line.match(/^(\s*)([^:#\s][^:]*)(:)(\s*)(.*)$/)
    if (kv) {
      const [, indent, key, colon, sp, rest] = kv
      return (
        <div key={i}>
          <span>{indent}</span>
          <span className="text-sky-300">{key}</span>
          <span className="text-slate-500">{colon}</span>
          <span>{sp}</span>
          <span className={rest.startsWith('"') || rest.startsWith("'") ? 'text-emerald-300' : 'text-amber-200'}>
            {rest}
          </span>
        </div>
      )
    }
    return <div key={i}>{line || '\u00a0'}</div>
  })
}

export function YamlPreview({ yamlText, filename }: Props) {
  const [copied, setCopied] = useState(false)
  const highlighted = useMemo(() => highlightYaml(yamlText), [yamlText])

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(yamlText)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // ponytail: clipboard may be unavailable in non-secure contexts
    }
  }

  const download = () => {
    const blob = new Blob([yamlText], { type: 'application/yaml;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="flex h-full min-h-0 flex-col rounded-lg border border-[#1f2937] bg-[#111827]">
      <div className="flex items-center justify-between border-b border-[#1f2937] px-3 py-2">
        <span className="text-xs font-medium text-slate-400">Live YAML preview</span>
        <div className="flex gap-1">
          <button
            type="button"
            onClick={copy}
            className="inline-flex items-center gap-1.5 rounded border border-[#1f2937] bg-[#090d16] px-2 py-1 text-xs text-slate-300 hover:border-[#3b82f6] hover:text-white"
          >
            {copied ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
            {copied ? 'Copied' : 'Copy YAML'}
          </button>
          <button
            type="button"
            onClick={download}
            className="inline-flex items-center gap-1.5 rounded border border-[#1f2937] bg-[#090d16] px-2 py-1 text-xs text-slate-300 hover:border-[#3b82f6] hover:text-white"
          >
            <Download className="h-3.5 w-3.5" />
            Download .yaml
          </button>
        </div>
      </div>
      <pre className="min-h-0 flex-1 overflow-auto p-4 font-mono text-xs leading-relaxed text-slate-200">
        {highlighted}
      </pre>
    </div>
  )
}
