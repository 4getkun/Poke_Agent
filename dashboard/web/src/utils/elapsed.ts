// formatElapsed renders a wall-clock duration since `from` as a tight
// human-readable string ("12s", "3m", "1h47m"). Used by both AgentCard's
// "X ago" footer and ChatPanel's busy-duration badge so the same agent
// reads identically across runtimes.
export function formatElapsed(from?: string | number | Date | null): string {
  if (!from) return ''
  const t = typeof from === 'number' ? from : new Date(from).getTime()
  if (!t) return ''
  const secs = Math.max(0, (Date.now() - t) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  return `${Math.floor(secs / 3600)}h${Math.floor((secs % 3600) / 60)}m`
}
