import { NavLink, Route, Routes } from 'react-router-dom'
import { cn } from '@/lib/cn'
import Monitor from '@/pages/Monitor'
import Editor from '@/pages/Editor'

const linkClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    'rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
    isActive
      ? 'bg-[#3b82f6]/15 text-[#3b82f6]'
      : 'text-slate-400 hover:bg-white/5 hover:text-slate-200',
  )

export default function App() {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-center justify-between border-b border-[#1f2937] bg-[#111827]/80 px-4 py-3 backdrop-blur">
        <div className="flex items-baseline gap-3">
          <span className="text-lg font-semibold tracking-tight text-white">The System</span>
          <span className="text-xs text-slate-500">Shadow-Diff</span>
        </div>
        <nav className="flex gap-1">
          <NavLink to="/" end className={linkClass}>
            Monitor
          </NavLink>
          <NavLink to="/editor" className={linkClass}>
            Editor
          </NavLink>
        </nav>
      </header>
      <main className="min-h-0 flex-1">
        <Routes>
          <Route path="/" element={<Monitor />} />
          <Route path="/editor" element={<Editor />} />
        </Routes>
      </main>
    </div>
  )
}
