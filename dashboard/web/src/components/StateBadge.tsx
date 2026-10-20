import { useEffect, useState } from 'react'
import { formatElapsed } from '../utils/elapsed'

// StateBadge renders the colored state pill (BUSY / IDLE / DONE / ERROR /
// CONN / NEEDS) used by both AgentCard and ChatPanel. Optional duration
// counter ticks every second when busySince is set.
//
// State strings come from the canonical status pipeline — both runtimes
// emit the same set, so this component is runtime-agnostic.

export type AgentLifecycleState =
  | 'busy'
  | 'idle'
  | 'done'
  | 'error'
  | 'needs_input'
  | 'connecting'
  // Visible during an intentional restart (chat /model, /effort) so the
  // disconnect-then-reconnect dance reads as deliberate instead of broken.
  | 'reconfiguring'

interface StateBadgeProps {
  state: AgentLifecycleState
  /** ISO timestamp (or epoch ms) for when the agent went busy. When set
   *  AND `state === 'busy'`, the badge appends a live duration like "12s". */
  busySince?: string | number | null
  /** Smaller pixel size — used inside the agent card header. */
  compact?: boolean
}

const STATE_LABEL: Record<AgentLifecycleState, string> = {
  busy: 'BUSY',
  idle: 'IDLE',
  done: 'DONE',
  error: 'ERR',
  needs_input: 'NEEDS',
  connecting: 'CONN',
  reconfiguring: 'RECONFIG',
}

const STATE_BG: Record<AgentLifecycleState, string> = {
  busy: 'bg-accent-red animate-pulse-soft',
  idle: 'theme-bg-panel-subtle',
  done: 'bg-accent-green',
  error: 'bg-accent-red',
  needs_input: 'bg-accent-yellow animate-pulse-soft',
  connecting: 'bg-accent-yellow',
  reconfiguring: 'bg-accent-blue animate-pulse-soft',
}

export function StateBadge({ state, busySince, compact }: StateBadgeProps) {
  // Tick every second while busy so the duration string stays live.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (state !== 'busy' || !busySince) return
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [state, busySince])

  const label = STATE_LABEL[state] ?? state.toUpperCase()
  const bg = STATE_BG[state] ?? 'theme-bg-panel-subtle'
  const elapsed = state === 'busy' && busySince ? formatElapsed(busySince) : ''
  const px = compact ? 'px-1.5 py-px text-xs' : 'px-2 py-px text-s'

  return (
    <span
      className={`theme-font-display ${px} rounded-full leading-none ${bg} theme-text-primary`}
      style={{ textShadow: 'var(--theme-text-shadow-pixel)' }}
    >
      {label}
      {elapsed && <span className="ml-1 opacity-80">{elapsed}</span>}
    </span>
  )
}
