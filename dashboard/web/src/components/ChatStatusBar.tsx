import { useEffect, useState } from 'react'
import { AgentState } from '../types'
import { BgShell, BgShellStatus, BgTaskType, truncateCommand } from '../utils/bgShells'
import { formatElapsed } from '../utils/elapsed'

// ChatStatusBar renders below the ChatPanel's input box. Two zones:
//
//   Top line: profile / permissions / model / effort — mirrors what the
//             Claude CLI shows below its prompt (🏠 Personal · bypass on).
//   Optional second zone: a list of currently-running background shells
//             (`Bash(run_in_background=true)`) tracked through their
//             BashOutput / KillShell follow-ups. Rows for completed/
//             killed/failed shells linger ~5s before clearing.
//
// Visual: tight one-liner header + per-shell rows with status dot, command
// preview, and live elapsed timer. Click a shell to expand its last
// captured output.

interface ChatStatusBarProps {
  agent: AgentState
  shells: BgShell[]
  children?: React.ReactNode
}

const STATUS_DOT: Record<BgShellStatus, string> = {
  running: 'bg-accent-yellow animate-pulse-soft',
  completed: 'bg-accent-green',
  failed: 'bg-accent-red',
  killed: 'theme-bg-panel-subtle',
}

const STATUS_LABEL: Record<BgShellStatus, string> = {
  running: 'running',
  completed: 'done',
  failed: 'failed',
  killed: 'killed',
}

export function ChatStatusBar({ agent, shells, children }: ChatStatusBarProps) {
  // Profile label: prefer an explicit role+project combination, fall back
  // to profile_name. Mirrors the resolution logic in pokegent.sh launch.
  const profileLabel = (() => {
    if (agent.role && agent.project) return `${agent.role} · ${agent.project}`
    if (agent.project) return agent.project
    return agent.profile_name || ''
  })()
  // Project / profile color tints the indicator dot to match the
  // AgentCard's profile pill visual language.
  const [r, g, b] = agent.project_color || agent.color || [100, 100, 100]
  const dotStyle = { background: `rgba(${r},${g},${b},0.85)` }


  return (
    <div className="shrink-0 px-3 py-1.5 border-t theme-border-subtle text-m theme-font-mono theme-text-muted select-none">
      <div className="flex items-center gap-3">
        {agent.model && (
          <span className="theme-text-muted">{agent.model}</span>
        )}
        {agent.effort && (
          <span className="theme-text-muted">effort: {agent.effort}</span>
        )}
        {children && <span className="ml-auto">{children}</span>}
      </div>

      {shells.length > 0 && <BgShellSummary shells={shells} />}
    </div>
  )
}

function BgShellSummary({ shells }: { shells: BgShell[] }) {
  const [hover, setHover] = useState(false)
  const running = shells.filter(s => s.status === 'running')
  const done = shells.filter(s => s.status !== 'running')
  const [, setTick] = useState(0)
  useEffect(() => {
    if (running.length === 0) return
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [running.length])

  const label = (() => {
    const active = running.length
    const finished = done.length
    if (active === 0 && finished === 0) return ''
    const parts: string[] = []
    const runAgents = running.filter(s => s.type === 'agent').length
    const runMonitors = running.filter(s => s.type === 'monitor').length
    const runShells = active - runAgents - runMonitors
    if (runShells > 0) parts.push(`${runShells} bg task${runShells > 1 ? 's' : ''}`)
    if (runAgents > 0) parts.push(`${runAgents} subagent${runAgents > 1 ? 's' : ''}`)
    if (runMonitors > 0) parts.push(`${runMonitors} monitor${runMonitors > 1 ? 's' : ''}`)
    if (parts.length > 0) return parts.join(', ')
    return `${finished} completed`
  })()

  return (
    <div className="relative mt-1" onMouseEnter={() => setHover(true)} onMouseLeave={() => setHover(false)}>
      <div className="flex items-center gap-2 px-1.5 py-0.5 cursor-default">
        <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${running.length > 0 ? 'bg-accent-yellow animate-pulse-soft' : 'bg-accent-green'}`} />
        <span className="theme-text-muted text-m theme-font-mono">{label}</span>
      </div>
      {hover && (
        <div className="absolute bottom-full left-0 right-0 mb-1 bg-surface-1 border theme-border-subtle rounded-md shadow-lg p-2 space-y-1 z-50">
          {shells.map(s => {
            const elapsed = s.status === 'running'
              ? formatElapsed(s.startedAt)
              : (s.endedAt && s.startedAt ? Math.max(0, Math.floor((s.endedAt - s.startedAt) / 1000)) + 's' : '')
            const typeLabel = s.type === 'agent' ? 'agent' : s.type === 'monitor' ? 'monitor' : 'shell'
            return (
              <div key={s.taskId} className="flex items-center gap-2 text-m theme-font-mono">
                <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${STATUS_DOT[s.status]}`} />
                <span className="theme-text-faint text-s shrink-0">{typeLabel}</span>
                <span className="theme-text-secondary truncate flex-1">{truncateCommand(s.command, 45)}</span>
                <span className="theme-text-faint text-m shrink-0 tabular-nums">{STATUS_LABEL[s.status]} {elapsed}</span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
