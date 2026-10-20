// Background-shell lifecycle parser.
//
// Claude's CLI shows a footer of running background tasks (Bash invocations
// where `run_in_background: true`). The Claude SDK exposes three tool calls
// involved:
//
//   • Bash(run_in_background=true)        spawns the shell, returns a task_id
//   • BashOutput(task_id)                 polls; output indicates exit/still-running
//   • KillShell(task_id)                  terminates a running task
//
// (`bash_id` / `shell_id` / `agentId` are legacy SDK aliases for `task_id` —
// we accept any of them when parsing.)
//
// ACP forwards each as a tool_call / tool_call_update event. We watch those
// to maintain a side-state of currently-active shells, then render them in
// the StatusBar below the input. Pure functions here — keeps ChatPanel's
// applyUpdate readable and these testable.
//
// Lifecycle states:
//   running    initial state after Bash(run_in_background=true) succeeds
//   completed  BashOutput returned with completion marker, or natural exit
//   failed     BashOutput returned with non-zero exit
//   killed     KillShell returned successfully

export type BgShellStatus = 'running' | 'completed' | 'failed' | 'killed'
export type BgTaskType = 'shell' | 'agent' | 'monitor'

export interface BgShell {
  taskId: string
  command: string
  startedAt: number
  status: BgShellStatus
  type?: BgTaskType
  endedAt?: number
  /** Last known stdout snippet, populated from BashOutput results. */
  lastOutput?: string
  /** Exit code, when known. */
  exitCode?: number
}

/** Extract a task/shell/bash id from an ACP tool_call payload. The SDK
 *  serializes background-task ids into rawOutput (sometimes structured,
 *  sometimes as `"task_id":"…"` text). We accept all known field names.
 */
export function extractTaskId(...candidates: unknown[]): string | null {
  for (const v of candidates) {
    if (v == null) continue
    // Direct field match on objects.
    if (typeof v === 'object') {
      const obj = v as Record<string, unknown>
      for (const k of ['task_id', 'bash_id', 'shell_id', 'agentId']) {
        const val = obj[k]
        if (typeof val === 'string' && val) return val
      }
    }
    // String fallback — extract id from JSON-ish or natural-language output.
    const s = typeof v === 'string' ? v : (() => {
      try { return JSON.stringify(v) } catch { return '' }
    })()
    if (!s) continue
    const m = s.match(/"(?:task_id|bash_id|shell_id|agentId)"\s*:\s*"([^"]+)"/)
      || s.match(/\b(shell_[A-Za-z0-9_-]{4,}|bash_[A-Za-z0-9_-]{4,}|task_[A-Za-z0-9_-]{4,})\b/)
    if (m) return m[1]
  }
  return null
}

/** Decide whether a tool_call is a "spawn a background shell" event.
 *  The SDK marks Bash with `run_in_background: true` in rawInput. */
export function isBackgroundSpawn(kind: string | undefined, rawInput: unknown): boolean {
  if (kind !== 'execute') return false
  if (rawInput && typeof rawInput === 'object') {
    return (rawInput as Record<string, unknown>).run_in_background === true
  }
  return false
}

/** Detect Agent (subagent) tool calls — these run in the background. */
export function isAgentSpawn(title: string | undefined, rawInput: unknown, kind?: string): boolean {
  if (kind === 'think') return true
  const t = (title || '').toLowerCase()
  if (t.includes('agent')) return true
  if (rawInput && typeof rawInput === 'object') {
    const inp = rawInput as Record<string, unknown>
    if (inp.subagent_type || inp.prompt) return true
    if (inp.run_in_background === true && (inp.description || inp.prompt)) return true
  }
  return false
}

/** Detect Monitor tool calls — long-running background watchers. */
export function isMonitorSpawn(title: string | undefined, kind: string | undefined): boolean {
  const t = (title || '').toLowerCase()
  return t.includes('monitor') || (kind === 'other' && t.includes('watch'))
}

/** Extract a short label for a background task from its rawInput. */
export function extractBgLabel(title: string | undefined, rawInput: unknown): string {
  if (rawInput && typeof rawInput === 'object') {
    const inp = rawInput as Record<string, unknown>
    if (typeof inp.description === 'string' && inp.description) return inp.description
    if (typeof inp.subagent_type === 'string') return `${inp.subagent_type} agent`
  }
  return title || 'background task'
}

/** Decide whether a tool_call is a BashOutput / KillShell follow-up.
 *  We check the title — the SDK sets it to the human-readable tool name. */
export function followupTool(title: string | undefined, kind: string | undefined): 'output' | 'kill' | null {
  const t = (title || '').toLowerCase()
  if (t.includes('bashoutput') || t.includes('bash_output') || t.includes('background output')
      || t.includes('taskoutput') || t.includes('task_output') || t.includes('task output')) {
    return 'output'
  }
  if (t.includes('killshell') || t.includes('kill_shell') || t.includes('kill shell')
      || t.includes('stop task') || t.includes('taskstop') || t.includes('task_stop')) {
    return 'kill'
  }
  // Some adapters set kind="other" for both — so as a fallback, look at rawInput
  // shape later in the caller. Return null here when title alone is ambiguous.
  if (kind === 'other') return null
  return null
}

/** Extract the command string from a Bash tool's rawInput, defensively. */
export function extractBashCommand(rawInput: unknown): string {
  if (rawInput && typeof rawInput === 'object') {
    const cmd = (rawInput as Record<string, unknown>).command
    if (typeof cmd === 'string') return cmd
  }
  return ''
}

/** Read the textual output from an ACP tool's `content` array (the
 *  structured form used in tool_call_update events). Returns the
 *  concatenated text content, empty string if absent. */
export function readToolText(content: unknown): string {
  if (!Array.isArray(content)) return ''
  const parts: string[] = []
  for (const item of content) {
    if (!item || typeof item !== 'object') continue
    const it = item as Record<string, any>
    if (it.type === 'content' && it.content?.type === 'text' && typeof it.content.text === 'string') {
      parts.push(it.content.text)
    } else if (it.type === 'terminal' && typeof it.terminalOutput === 'string') {
      parts.push(it.terminalOutput)
    } else if (typeof it.text === 'string') {
      parts.push(it.text)
    }
  }
  return parts.join('\n')
}

/** Derive completion state from a BashOutput tool's text result.
 *  Returns null when the shell is still running (output came back but
 *  the process is alive). Returns {status, exitCode?} when the shell
 *  has terminated.
 *
 *  The SDK's BashOutput result text typically contains markers like:
 *    "Process completed with exit code 0"
 *    "Status: completed"
 *    "exit code 1"
 *    "<exited>"
 *  When the shell's still going it has "Status: running" / no exit
 *  marker.
 */
export function parseBashOutputCompletion(text: string): { status: BgShellStatus; exitCode?: number } | null {
  if (!text) return null
  const lower = text.toLowerCase()
  // Killed wins over completed/failed.
  if (/\b(killed|terminated by user|stopped manually)\b/.test(lower)) {
    return { status: 'killed' }
  }
  // Pull "exit code N" if present — most reliable signal.
  const exitMatch = text.match(/exit\s*code[:\s]+(-?\d+)/i)
  if (exitMatch) {
    const code = parseInt(exitMatch[1], 10)
    return { status: code === 0 ? 'completed' : 'failed', exitCode: code }
  }
  if (/\b(status|state)[:\s]+completed\b/.test(lower) || /\bprocess (?:exited|completed|finished)\b/.test(lower)) {
    return { status: 'completed' }
  }
  if (/\b(status|state)[:\s]+failed\b/.test(lower)) {
    return { status: 'failed' }
  }
  // Default: assume still running.
  return null
}

/** Truncate command for display in the tight footer row. */
export function truncateCommand(cmd: string, max = 60): string {
  if (cmd.length <= max) return cmd
  return cmd.slice(0, max - 1) + '…'
}
