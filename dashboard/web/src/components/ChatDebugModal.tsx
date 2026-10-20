import { useEffect, useState } from 'react'
import { AgentState } from '../types'
import { BgShell } from '../utils/bgShells'

export function DebugModal({
  agent, pokegentId, streamReady, queuedMessages, bgShells, debugLog,
  onClose, onForceIdle, onRespawnAcp, onReconnectSse, onReloadTranscript, onFlushQueue, onClearBgTasks,
  showTimestamps, onToggleDebugBorders,
}: {
  agent: AgentState
  pokegentId: string
  streamReady: boolean
  queuedMessages: string[]
  bgShells: Map<string, BgShell>
  debugLog: string[]
  onClose: () => void
  onForceIdle: () => void
  onRespawnAcp: () => void
  onReconnectSse: () => void
  onReloadTranscript: () => void
  onFlushQueue: () => void
  onClearBgTasks: () => void
  showTimestamps?: boolean | 'debug'
  onToggleDebugBorders?: () => void
}) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [])

  const btnClass = "px-3 py-1.5 text-l theme-font-mono rounded theme-bg-panel-hover transition-colors text-left"

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 theme-modal-scrim" />
      <div
        className="relative rounded-lg overflow-hidden w-[520px] max-h-[80vh] flex flex-col"
        style={{ background: 'var(--theme-panel-bg)', border: '1px solid var(--theme-panel-divider)', boxShadow: 'var(--theme-shadow-strong)' }}
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-2.5 border-b theme-border-subtle">
          <span className="text-l theme-font-mono theme-text-warning">Debug — {agent.display_name || pokegentId.slice(0, 8)}</span>
          <button onClick={onClose} className="theme-text-faint theme-hover-text-primary text-xl">✕</button>
        </div>

        <div className="overflow-y-auto p-4 space-y-4 text-l theme-font-mono">
          {/* State */}
          <div>
            <div className="theme-text-faint text-m uppercase tracking-wider mb-1.5">State</div>
            <div className="grid grid-cols-2 gap-x-4 gap-y-1 theme-text-secondary">
              <span className="theme-text-faint">agent.state</span><span>{agent.state || '—'}</span>
              <span className="theme-text-faint">agent.detail</span><span className="truncate">{agent.detail || '—'}</span>
              <span className="theme-text-faint">isBusy</span><span className={agent.state === 'busy' ? 'text-accent-red' : 'text-accent-green'}>{String(agent.state === 'busy')}</span>
              <span className="theme-text-faint">streamReady</span><span className={streamReady ? 'text-accent-green' : 'text-accent-red'}>{String(streamReady)}</span>
              <span className="theme-text-faint">pokegentId</span><span className="truncate">{pokegentId}</span>
              <span className="theme-text-faint">sessionId</span><span className="truncate">{agent.session_id || '—'}</span>
              <span className="theme-text-faint">model</span><span>{agent.model || '—'}</span>
              <span className="theme-text-faint">context</span><span>{agent.context_tokens?.toLocaleString() || '?'} / {agent.context_window?.toLocaleString() || '?'}</span>
              <span className="theme-text-faint">interface</span><span>{agent.interface || '—'}</span>
              <span className="theme-text-faint">queued</span><span>{queuedMessages.length}</span>
              <span className="theme-text-faint">bgTasks</span><span>{bgShells.size} ({[...bgShells.values()].filter(s => s.status === 'running').length} running)</span>
            </div>
          </div>

          {/* Actions */}
          <div>
            <div className="theme-text-faint text-m uppercase tracking-wider mb-1.5">Actions</div>
            <div className="grid grid-cols-2 gap-1.5">
              <button onClick={onForceIdle} className={`${btnClass} theme-text-warning border theme-border-subtle`}>
                Force idle
                <div className="text-m theme-text-faint mt-0.5">Override state to idle + broadcast idle</div>
              </button>
              <button onClick={onRespawnAcp} className={`${btnClass} theme-text-warning border theme-border-subtle`}>
                Respawn ACP
                <div className="text-m theme-text-faint mt-0.5">Kill + relaunch ACP subprocess</div>
              </button>
              <button onClick={onReconnectSse} className={`${btnClass} theme-text-accent border theme-border-subtle`}>
                Reconnect SSE
                <div className="text-m theme-text-faint mt-0.5">Force close + reopen event stream</div>
              </button>
              <button onClick={onReloadTranscript} className={`${btnClass} theme-text-accent border theme-border-subtle`}>
                Reload transcript
                <div className="text-m theme-text-faint mt-0.5">Re-fetch transcript from disk</div>
              </button>
              <button onClick={onFlushQueue} className={`${btnClass} theme-text-muted border theme-border-subtle`}>
                Flush queue ({queuedMessages.length})
                <div className="text-m theme-text-faint mt-0.5">Discard all queued messages</div>
              </button>
              <button onClick={onClearBgTasks} className={`${btnClass} theme-text-muted border theme-border-subtle`}>
                Clear bg tasks ({bgShells.size})
                <div className="text-m theme-text-faint mt-0.5">Reset background task list</div>
              </button>
              {onToggleDebugBorders && (
                <button onClick={onToggleDebugBorders} className={`${btnClass} ${showTimestamps === 'debug' ? 'theme-text-danger border theme-border-danger' : 'theme-text-muted border theme-border-subtle'}`}>
                  {showTimestamps === 'debug' ? 'Hide' : 'Show'} layout borders
                  <div className="text-m theme-text-faint mt-0.5">Show table cell borders for debugging</div>
                </button>
              )}
            </div>
          </div>

          {/* Event log */}
          <div>
            <div className="theme-text-faint text-m uppercase tracking-wider mb-1.5">Event log</div>
            <div className="theme-bg-panel-muted rounded p-2 max-h-[200px] overflow-y-auto">
              {debugLog.length === 0 ? (
                <span className="theme-text-faint italic">No events yet — interact with the agent to see logs</span>
              ) : (
                debugLog.map((line, i) => (
                  <div key={i} className="text-m theme-text-muted leading-snug">{line}</div>
                ))
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
