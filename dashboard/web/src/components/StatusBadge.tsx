import { useEffect, useState } from 'react'

// GBA-style status condition pills — solid bg + white text, like BRN/PSN/SLP in Pokemon
const STATUS_CONFIG: Record<string, { label: string; bg: string; timeColor: string; pulse?: boolean }> = {
  idle:        { label: 'SLP',  bg: 'var(--theme-status-idle)',  timeColor: 'theme-text-faint' },
  busy:        { label: 'ATK',  bg: 'var(--theme-status-busy)',  timeColor: 'theme-text-muted', pulse: true },
  done:        { label: 'OK',   bg: 'var(--theme-status-done)',  timeColor: 'theme-text-faint' },
  needs_input: { label: 'WAIT', bg: 'var(--theme-status-needs-input)',  timeColor: 'theme-text-muted', pulse: true },
  error:       { label: 'PSN',  bg: 'var(--theme-status-error)',  timeColor: 'theme-text-muted', pulse: true },
  starting:    { label: 'NEW',  bg: 'var(--theme-status-starting)',  timeColor: 'theme-text-muted', pulse: true },
}

function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.floor(seconds)}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

function getTimeLabel(status: string, seconds: number): string {
  if (status === 'busy') return formatDuration(seconds)
  if (status === 'idle') return formatDuration(seconds)
  if (status === 'needs_input') return formatDuration(seconds)
  return formatDuration(seconds)
}

interface StatusBadgeProps {
  status: string
  lastUpdated?: string
  busySince?: string
}

export function StatusBadge({ status, lastUpdated, busySince }: StatusBadgeProps) {
  const [now, setNow] = useState(Date.now())

  useEffect(() => {
    const interval = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(interval)
  }, [])

  // For busy state, count from when the prompt was issued (busySince),
  // not from the last hook event (lastUpdated)
  const timeRef = (status === 'busy' && busySince) ? busySince : lastUpdated
  const seconds = timeRef
    ? Math.max(0, (now - new Date(timeRef).getTime()) / 1000)
    : 0

  // Phase 2: done collapsed into idle — normalize any lingering "done" from hooks
  const effectiveStatus = status === 'done' ? 'idle' : status
  const config = STATUS_CONFIG[effectiveStatus] || STATUS_CONFIG.idle
  const timeLabel = timeRef ? getTimeLabel(effectiveStatus, seconds) : ''

  return (
    <div className="flex flex-col items-end gap-0.5 shrink-0">
      <span
        className={`inline-flex items-center justify-center text-s theme-font-display theme-text-primary px-2 py-0.5 rounded-full leading-none ${config.pulse ? 'animate-pulse-soft' : ''}`}
        style={{
          backgroundColor: config.bg,
          textShadow: 'var(--theme-text-shadow-pixel)',
          boxShadow: 'var(--theme-shadow-panel)',
          minWidth: 28,
          textAlign: 'center',
        }}
      >
        {config.label}
      </span>
      {timeLabel && (
        <span className={`text-s theme-font-mono ${config.timeColor}`}>
          {timeLabel}
        </span>
      )}
    </div>
  )
}
