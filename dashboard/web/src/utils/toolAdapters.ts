import type { ReactNode } from 'react'

// ── Tool Adapter Interface ────────────────────────────────
//
// Each adapter handles a class of tool calls. It provides:
//   1. Parsing: extract structured info from rawInput/rawOutput
//   2. Header: one-line summary for the collapsed tool call row
//   3. File ops: what files were touched (for the Files tab)
//
// Rendering is handled by ToolCallRow using the parsed data —
// adapters don't return JSX, keeping them testable and decoupled.

export interface FileOp {
  path: string
  verb: 'read' | 'edited' | 'created' | 'accessed' | 'notebook'
  diffSummary: string
  cellId?: string
}

export interface ToolHeader {
  label: string
  detail: string
  filePath?: string
  description?: string
}

export interface DiffInfo {
  oldStr: string
  newStr: string
  filePath: string
}

export interface ParsedTool {
  header: ToolHeader
  fileOps: FileOp[]
  effect?: 'read' | 'write' | 'execute' | 'agent' | 'unknown'
  diff?: DiffInfo
  patch?: string
  bashCommand?: string
  bashDescription?: string
  agentDescription?: string
  summaryOverride?: string
}

export interface ToolAdapter {
  matches(toolName: string, kind: string | undefined, inp: Record<string, unknown> | null): boolean
  parse(toolName: string, tc: RawToolCall): ParsedTool
}

export interface RawToolCall {
  toolCallId: string
  title?: string
  kind?: string
  status?: string
  locations?: { path: string; line?: number }[]
  rawInput?: unknown
  rawOutput?: unknown
  content?: unknown[]
  _meta?: { claudeCode?: { toolName?: string; toolResponse?: Record<string, unknown> } }
}

function inp(tc: RawToolCall): Record<string, unknown> | null {
  return tc.rawInput && typeof tc.rawInput === 'object' ? tc.rawInput as Record<string, unknown> : null
}

function asText(value: unknown): string {
  if (typeof value === 'string') return value
  if (value == null) return ''
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function shortPath(p: unknown): string {
  return asText(p).replace(/.*\/Projects\//, '').replace(/.*\/node_modules\//, 'node_modules/')
}

function trunc(s: unknown, n = 80): string {
  const text = asText(s)
  return text.length > n ? text.slice(0, n) + '…' : text
}

function firstTitleToken(title?: unknown): string {
  const text = asText(title)
  if (!text) return ''
  return text.split(':', 1)[0].trim()
}

function titleDetail(title?: unknown): string {
  const text = asText(title)
  if (!text) return ''
  const idx = text.indexOf(':')
  return idx >= 0 ? text.slice(idx + 1).trim() : ''
}

const knownTitlePrefixes = ['Search', 'Read', 'List', 'Write', 'Update', 'Notebook', 'Bash', 'Run', 'Git', 'Agent', 'Fetch', 'Input', 'Output']

function titleVerb(title?: string): string {
  const token = firstTitleToken(title)
  if (!token) return ''
  if (knownTitlePrefixes.includes(token)) return token
  for (const prefix of knownTitlePrefixes) {
    if (token.startsWith(`${prefix} `) || token.startsWith(`${prefix}\t`) || token.startsWith(`${prefix},`)) return prefix
  }
  return token
}

function titleAfterVerb(title?: string): string {
  const token = firstTitleToken(title)
  if (!token) return ''
  for (const prefix of knownTitlePrefixes) {
    if (token === prefix) return titleDetail(title)
    if (token.startsWith(`${prefix} `) || token.startsWith(`${prefix}\t`) || token.startsWith(`${prefix},`)) {
      return token.slice(prefix.length).replace(/^[\s,]+/, '').trim()
    }
  }
  return titleDetail(title)
}

function firstLocation(tc: RawToolCall): string {
  return tc.locations?.find(l => l.path)?.path || ''
}

function toolNameFor(tc: RawToolCall): string {
  const metaName = tc._meta?.claudeCode?.toolName
  if (metaName) return metaName
  const titleName = titleVerb(tc.title)
  if (titleName && titleName !== 'exec_command') return titleName
  return tc.kind || titleName || 'tool'
}

function inputString(i: Record<string, unknown> | null, ...keys: string[]): string {
  if (!i) return ''
  for (const key of keys) {
    const value = i[key]
    if (typeof value === 'string' && value !== '') return value
    if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  }
  return ''
}

function rawInputString(tc: RawToolCall): string {
  return typeof tc.rawInput === 'string' ? tc.rawInput : ''
}

function extractJsonStringField(raw: string, key: string): string {
  if (!raw) return ''
  const re = new RegExp(`"${key}"\\s*:\\s*"`)
  const match = re.exec(raw)
  if (!match) return ''

  let buf = ''
  let escaped = false
  for (let i = match.index + match[0].length; i < raw.length; i++) {
    const ch = raw[i]
    if (escaped) {
      buf += `\\${ch}`
      escaped = false
      continue
    }
    if (ch === '\\') {
      escaped = true
      continue
    }
    if (ch === '"') break
    buf += ch
  }
  // Server-side transcript tails may truncate long Codex function-call JSON.
  // Decode the valid prefix so Files/Commands still work even if the closing
  // quote/brace was cut off.
  buf = buf.replace(/\.\.\.$/, '')
  try {
    return JSON.parse(`"${buf.replace(/"$/, '')}"`)
  } catch {
    return buf
      .replace(/\\n/g, '\n')
      .replace(/\\t/g, '\t')
      .replace(/\\"/g, '"')
      .replace(/\\\\/g, '\\')
  }
}

function rawInputFieldString(tc: RawToolCall, ...keys: string[]): string {
  const raw = rawInputString(tc)
  if (!raw) return ''
  try {
    const parsed = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') {
      const value = inputString(parsed as Record<string, unknown>, ...keys)
      if (value) return value
    }
  } catch {
    // Fall through to prefix extraction for truncated JSON.
  }
  for (const key of keys) {
    const value = extractJsonStringField(raw, key)
    if (value) return value
  }
  return ''
}

function resolvePath(path: string, workdir: string): string {
  if (!path) return ''
  if (path.startsWith('/')) return path
  if (!workdir.startsWith('/')) return path
  const base = workdir.endsWith('/') ? workdir.slice(0, -1) : workdir
  return `${base}/${path}`.replace(/\/\.\//g, '/')
}

function firstReadablePath(cmd: string, workdir: string): string {
  const patterns = [
    /\b(?:cat|head|tail|less|nl)\b(?:\s+-[^\s]+)*\s+["']?([^"'\s|;&<>]+)/,
    /\bsed\b(?:\s+-[^\s]+)*\s+(?:"[^"]*"|'[^']*'|[^\s]+)\s+["']?([^"'\s|;&<>]+)/,
    /\b(?:awk)\b\s+(?:"[^"]*"|'[^']*'|[^\s]+)\s+["']?([^"'\s|;&<>]+)/,
  ]
  for (const re of patterns) {
    const match = cmd.match(re)
    if (match?.[1] && !match[1].startsWith('-')) return resolvePath(match[1], workdir)
  }
  return ''
}


function cleanCommandPath(path: string): string {
  return path
    .trim()
    .replace(/^['"]|['"]$/g, '')
    .replace(/[),;]+$/g, '')
}

function looksLikeSourceFile(path: string): boolean {
  return /\.(?:py|ipynb|md|mdx|ts|tsx|js|jsx|go|rs|astro|java|cpp|c|h|hpp|json|jsonl|yaml|yml|toml|sh|bash|zsh|sql|css|scss|html|txt)$/i.test(path)
}

function uniquePaths(paths: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of paths) {
    const path = cleanCommandPath(raw)
    if (!path || seen.has(path)) continue
    seen.add(path)
    out.push(path)
  }
  return out
}

type PatchStat = { op: 'Add' | 'Update' | 'Delete'; added: number; removed: number }

function summarizePatchStat(stat: PatchStat | undefined): string {
  if (!stat) return ''
  const parts: string[] = []
  if (stat.added) parts.push(`+${stat.added}`)
  if (stat.removed) parts.push(`-${stat.removed}`)
  if (parts.length > 0) return `${parts.join(' ')} lines`
  if (stat.op === 'Add') return 'created'
  if (stat.op === 'Delete') return 'deleted'
  return 'edited'
}

function patchStatsByPath(patch: string, workdir: string): Map<string, PatchStat> {
  const stats = new Map<string, PatchStat>()
  let current: string | null = null

  for (const line of patch.split('\n')) {
    const header = line.match(/^\*\*\* (Add|Update|Delete) File:\s+(.+?)\s*$/)
    if (header) {
      current = resolvePath(header[2].trim(), workdir)
      stats.set(current, { op: header[1] as PatchStat['op'], added: 0, removed: 0 })
      continue
    }
    if (!current) continue
    const stat = stats.get(current)
    if (!stat) continue
    if (line.startsWith('+') && !line.startsWith('+++')) stat.added++
    if (line.startsWith('-') && !line.startsWith('---')) stat.removed++
  }

  return stats
}

function pathsWrittenByCommand(cmd: string, workdir: string): string[] {
  const paths: string[] = []
  const add = (raw: string | undefined) => {
    if (!raw) return
    const cleaned = cleanCommandPath(raw)
    if (!cleaned || cleaned.startsWith('-')) return
    if (!looksLikeSourceFile(cleaned)) return
    paths.push(resolvePath(cleaned, workdir))
  }

  for (const m of cmd.matchAll(/(?:>|>>|tee\s+(?:-[a-zA-Z]+\s+)*)\s*["']?([^"'\s|;&<>]+\.(?:ipynb|mdx|tsx|jsx|jsonl|yaml|astro|java|cpp|hpp|bash|zsh|scss|html|toml|json|yml|py|md|ts|js|go|rs|sh|sql|css|txt|c|h))/g)) add(m[1])
  for (const m of cmd.matchAll(/(?:open|Path)\(\s*["']([^"']+)["']\s*,\s*["'](?:w|a|x|w\+|a\+)/g)) add(m[1])
  for (const m of cmd.matchAll(/Path\(\s*["']([^"']+)["']\s*\)\.write_(?:text|bytes)\s*\(/g)) add(m[1])
  for (const m of cmd.matchAll(/(?:write_text|write_bytes)\s*\([^)]*?path\s*=\s*["']([^"']+)["']/g)) add(m[1])
  for (const path of patchStatsByPath(cmd, workdir).keys()) add(path)
  const pathVars = new Map<string, string>()
  for (const m of cmd.matchAll(/(?:^|\n)\s*([A-Za-z_]\w*)\s*=\s*Path\(\s*["']([^"']+)["']\s*\)/g)) {
    pathVars.set(m[1], m[2])
  }
  for (const m of cmd.matchAll(/(?:^|\n)\s*([A-Za-z_]\w*)\s*=\s*["']([^"']+\.(?:ipynb|mdx|tsx|jsx|jsonl|yaml|astro|java|cpp|hpp|bash|zsh|scss|html|toml|json|yml|py|md|ts|js|go|rs|sh|sql|css|txt|c|h))["']/g)) {
    pathVars.set(m[1], m[2])
  }
  for (const [name, path] of pathVars) {
    if (new RegExp(`\\b${name}\\.write_(?:text|bytes)\\s*\\(`).test(cmd)) add(path)
    if (new RegExp(`open\\s*\\(\\s*${name}\\s*,\\s*["'](?:w|a|x|w\\+|a\\+)`).test(cmd)) add(path)
  }
  for (const m of cmd.matchAll(/nbformat\.write\s*\([^,]+,\s*["']([^"']+\.ipynb)["']/g)) add(m[1])
  for (const m of cmd.matchAll(/nbformat\.write\s*\([^,]+,\s*([A-Za-z_]\w*)\s*\)/g)) add(pathVars.get(m[1]))
  for (const m of cmd.matchAll(/["']([^"']+\.ipynb)["']/g)) {
    if (/nbformat|json\.dump|write|cells|notebook/i.test(cmd)) add(m[1])
  }

  return uniquePaths(paths)
}

function notebookPathFromCommand(cmd: string, workdir: string): string {
  if (!/\.ipynb/.test(cmd)) return ''
  if (!/(?:python3?|nbformat|json\.load|json\.dump|cells|notebook)/i.test(cmd)) return ''
  const m = cmd.match(/["']([^"']+\.ipynb)["']/) || cmd.match(/([^\s"']+\.ipynb)/)
  return m?.[1] ? resolvePath(m[1], workdir) : ''
}

function classifyCodexCommand(cmd: string): { label: string; detail: string; filePath?: string; description?: string } {
  const trimmed = cmd.trim()
  if (/^\s*(?:cat|head|tail|less|nl|sed|awk)\b/.test(trimmed)) {
    return { label: 'Read', detail: trunc(trimmed) }
  }
  if (/^\s*(?:rg|grep|find|ag)\b/.test(trimmed)) return { label: 'Search', detail: trunc(trimmed) }
  if (/^\s*(?:ls|tree)\b/.test(trimmed)) return { label: 'List', detail: trunc(trimmed) }
  if (/^\s*git\b/.test(trimmed)) return { label: 'Git', detail: trunc(trimmed) }
  if (/^\s*(?:python|python3|node|npm|pnpm|yarn|go|cargo|make|pytest|uv)\b/.test(trimmed)) return { label: 'Run', detail: trunc(trimmed) }
  return { label: 'Bash', detail: trunc(trimmed || 'shell command') }
}

// ── Adapters ──────────────────────────────────────────────

const codexExecAdapter: ToolAdapter = {
  matches(toolName, _kind, i) {
    return toolName === 'exec_command' || !!inputString(i, 'cmd', 'command')
  },
  parse(_, tc) {
    const i = inp(tc)
    const cmd = inputString(i, 'command', 'cmd') || rawInputFieldString(tc, 'command', 'cmd')
    const workdir = inputString(i, 'workdir') || rawInputFieldString(tc, 'workdir')
    const desc = inputString(i, 'description', 'justification') || rawInputFieldString(tc, 'description', 'justification')
    const notebookPath = notebookPathFromCommand(cmd, workdir)
    const writtenPaths = pathsWrittenByCommand(cmd, workdir)
    const patchStats = patchStatsByPath(cmd, workdir)
    const filePath = firstReadablePath(cmd, workdir)
    const classified = classifyCodexCommand(cmd)

    if (notebookPath) {
      return {
        header: { label: 'Notebook', detail: `${shortPath(notebookPath)} via command`, filePath: notebookPath, description: desc || 'notebook manipulation' },
        fileOps: [{ path: notebookPath, verb: 'edited', diffSummary: 'via command' }],
        effect: 'write',
        bashCommand: cmd,
        bashDescription: desc || 'notebook manipulation',
        summaryOverride: desc || `notebook script on ${shortPath(notebookPath)}`,
      }
    }

    if (writtenPaths.length > 0) {
      const first = writtenPaths[0]
      return {
        header: { label: 'Update', detail: writtenPaths.length > 1 ? `${shortPath(first)} +${writtenPaths.length - 1}` : shortPath(first), filePath: first, description: desc || 'file write command' },
        fileOps: writtenPaths.map(path => {
          const stat = patchStats.get(path)
          return {
            path,
            verb: stat?.op === 'Add' ? 'created' : 'edited',
            diffSummary: summarizePatchStat(stat) || 'unknown',
          }
        }),
        effect: 'write',
        bashCommand: cmd,
        bashDescription: desc || 'file write command',
        summaryOverride: writtenPaths.length > 1 ? `edited ${writtenPaths.length} files` : `edited ${shortPath(first)}`,
      }
    }

    if (filePath) {
      classified.label = 'Read'
      classified.filePath = filePath
      classified.detail = shortPath(filePath)
    }
    if (workdir && !filePath) {
      classified.description = `cwd ${shortPath(workdir)}`
    }

    return {
      header: { ...classified, description: desc || classified.description },
      fileOps: filePath ? [{ path: filePath, verb: 'read', diffSummary: '' }] : [],
      effect: filePath || classified.label === 'Read' || classified.label === 'Search' || classified.label === 'List' ? 'read' : 'execute',
      bashCommand: cmd,
      bashDescription: desc || classified.description,
    }
  },
}

const codexApplyPatchAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'apply_patch' },
  parse(_, tc) {
    const i = inp(tc)
    const patch = inputString(i, 'patch', 'input') || rawInputString(tc)
    const workdir = inputString(i, 'workdir')
    const stats = patchStatsByPath(patch, workdir)
    const ops = Array.from(stats.entries()).map(([path, stat]) => ({ op: stat.op, path }))
    const first = ops[0]?.path || ''
    const label = ops.length > 0 && ops.every(op => op.op === 'Add') ? 'Write' : 'Update'

    return {
      header: { label, detail: first ? shortPath(first) : 'patch', filePath: first.startsWith('/') ? first : undefined },
      fileOps: ops.map(({ op, path }) => ({ path, verb: op === 'Add' ? 'created' : 'edited', diffSummary: summarizePatchStat(stats.get(path)) })),
      effect: 'write',
      patch,
      summaryOverride: ops.length > 1 ? `updated ${ops.length} files` : summarizePatchStat(stats.get(first)),
    }
  },
}

const codexStdinAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'write_stdin' },
  parse(_, tc) {
    const i = inp(tc)
    const session = inputString(i, 'session_id')
    const chars = inputString(i, 'chars')
    return {
      header: { label: chars ? 'Input' : 'Output', detail: session ? `session ${session}` : 'running command' },
      fileOps: [],
      effect: 'execute',
      summaryOverride: chars ? 'sent input to running command' : 'read running command output',
    }
  },
}

const editAdapter: ToolAdapter = {
  matches(toolName, kind) {
    return toolName === 'Edit' || toolName === 'Update' || kind === 'edit'
  },
  parse(toolName, tc) {
    const i = inp(tc)
    const resp = tc._meta?.claudeCode?.toolResponse
    const rawPath = asText(i?.file_path || resp?.filePath || resp?.file_path || firstLocation(tc) || titleAfterVerb(tc.title) || '')
    const filePath = rawPath && rawPath.startsWith('/') ? rawPath : ''
    const oldStr = asText(i?.old_string || i?.oldString || '')
    const newStr = asText(i?.new_string || i?.newString || '')
    const diffOld = asText(resp?.oldString || resp?.old_string || oldStr)
    const diffNew = asText(resp?.newString || resp?.new_string || newStr)
    const diffFile = asText(resp?.filePath || resp?.file_path || filePath)

    let diffSummary = ''
    if (diffOld && diffNew) {
      const oL = diffOld.split('\n').length, nL = diffNew.split('\n').length
      const parts: string[] = []
      if (nL > oL) parts.push(`+${nL - oL}`)
      if (oL > nL) parts.push(`-${oL - nL}`)
      if (parts.length === 0) parts.push(`~${Math.min(oL, nL)}`)
      diffSummary = parts.join(' ') + ' lines'
    }

    return {
      header: { label: 'Update', detail: filePath ? shortPath(filePath) : trunc(rawPath), filePath },
      fileOps: filePath ? [{ path: filePath, verb: 'edited', diffSummary }] : [],
      effect: 'write',
      diff: diffOld && diffNew ? { oldStr: diffOld, newStr: diffNew, filePath: diffFile } : undefined,
    }
  }
}

const writeAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'Write' },
  parse(_, tc) {
    const i = inp(tc)
    const rawPath = asText(i?.file_path || firstLocation(tc) || titleAfterVerb(tc.title) || '')
    const filePath = rawPath && rawPath.startsWith('/') ? rawPath : ''
    const content = asText(i?.content || '')
    const lines = content ? content.split('\n').length : 0
    return {
      header: { label: 'Write', detail: filePath ? shortPath(filePath) : trunc(rawPath), filePath },
      fileOps: filePath ? [{ path: filePath, verb: 'created', diffSummary: lines ? `${lines} lines` : '' }] : [],
      effect: 'write',
    }
  }
}

const readAdapter: ToolAdapter = {
  matches(toolName, kind) { return toolName === 'Read' || toolName === 'List' || kind === 'read' },
  parse(toolName, tc) {
    const i = inp(tc)
    const raw = asText(i?.file_path || firstLocation(tc) || titleAfterVerb(tc.title) || '')
    const filePath = raw && raw.startsWith('/') ? raw : ''
    return {
      header: { label: toolName === 'List' ? 'List' : 'Read', detail: filePath ? shortPath(filePath) : trunc(raw), filePath: filePath || undefined },
      fileOps: filePath ? [{ path: filePath, verb: 'read', diffSummary: '' }] : [],
      effect: 'read',
    }
  }
}

const notebookEditAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'NotebookEdit' },
  parse(_, tc) {
    const i = inp(tc)
    const rawNb = asText(i?.notebook_path || i?.file_path || '')
    const filePath = rawNb && rawNb.startsWith('/') ? rawNb : ''
    const cellId = asText(i?.cell_id || '')
    const cellType = asText(i?.cell_type || 'code')
    const newSource = asText(i?.new_source || '')
    const editMode = asText(i?.edit_mode || 'replace')

    const lines = newSource.split('\n').length
    const verb = editMode === 'insert' ? 'created' : 'edited'
    const diffSummary = `cell ${cellId} (${cellType}) · ${lines} lines`

    const oldSource = ''
    const diff = newSource ? { oldStr: oldSource, newStr: newSource, filePath } : undefined

    return {
      header: {
        label: 'Notebook',
        detail: `${shortPath(filePath)} → cell ${cellId}`,
        filePath,
        description: `${editMode} ${cellType} cell`,
      },
      fileOps: filePath ? [{ path: filePath, verb: verb as FileOp['verb'], diffSummary, cellId }] : [],
      effect: 'write',
      diff,
      summaryOverride: `${editMode} ${cellType} cell ${cellId} (${lines} lines)`,
    }
  }
}

const bashAdapter: ToolAdapter = {
  matches(toolName, kind) { return toolName === 'Bash' || kind === 'execute' },
  parse(_, tc) {
    const i = inp(tc)
    const cmd = inputString(i, 'command', 'cmd') || titleAfterVerb(tc.title)
    const desc = inputString(i, 'description', 'justification')

    const fileOps: FileOp[] = []
    const fileMatch = cmd.match(/(?:cat|head|tail|less)\s+["']?([^\s"'|>]+)/)
    if (fileMatch) fileOps.push({ path: fileMatch[1], verb: 'read', diffSummary: '' })

    return {
      header: { label: 'Bash', detail: cmd.length > 80 ? cmd.slice(0, 80) + '…' : cmd },
      fileOps,
      effect: fileOps.length > 0 ? 'read' : 'execute',
      bashCommand: cmd,
      bashDescription: desc,
    }
  }
}

const bashNotebookAdapter: ToolAdapter = {
  matches(toolName, kind, i) {
    if (toolName !== 'Bash' && kind !== 'execute') return false
    const cmd = asText(i?.command || '')
    return /\.ipynb/.test(cmd) && (/python3?\s+-c/.test(cmd) || /nbformat/.test(cmd) || /json\.load/.test(cmd))
  },
  parse(_, tc) {
    const i = inp(tc)
    const cmd = asText(i?.command || '')
    const desc = asText(i?.description || '')
    const nbMatch = cmd.match(/['"]([^'"]*\.ipynb)['"]/) || cmd.match(/(\S+\.ipynb)/)
    const filePath = nbMatch ? nbMatch[1] : ''

    return {
      header: {
        label: 'Notebook',
        detail: filePath ? `${shortPath(filePath)} via python` : 'notebook manipulation',
        filePath: filePath || undefined,
        description: desc || 'python notebook script',
      },
      fileOps: filePath ? [{ path: filePath, verb: 'edited', diffSummary: 'via python' }] : [],
      effect: 'write',
      bashCommand: cmd,
      bashDescription: desc || 'notebook manipulation',
      summaryOverride: desc || `python script on ${shortPath(filePath || 'notebook')}`,
    }
  }
}

const agentAdapter: ToolAdapter = {
  matches(toolName, kind) { return toolName === 'Agent' || kind === 'think' },
  parse(_, tc) {
    const i = inp(tc)
    const desc = asText(i?.description || i?.prompt || '')
    return {
      header: { label: 'Agent', detail: desc.length > 60 ? desc.slice(0, 60) + '…' : desc },
      fileOps: [],
      effect: 'agent',
      agentDescription: desc,
    }
  }
}

const webFetchAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'WebFetch' || toolName === 'WebSearch' },
  parse(toolName, tc) {
    const i = inp(tc)
    const url = asText(i?.url || i?.query || '')
    const label = toolName === 'WebSearch' ? 'Search' : 'Fetch'
    let detail = url
    try { detail = new URL(url).hostname + new URL(url).pathname } catch { /* keep raw */ }
    if (detail.length > 70) detail = detail.slice(0, 67) + '…'
    return {
      header: { label, detail },
      fileOps: [],
      effect: 'read',
    }
  }
}

const grepGlobAdapter: ToolAdapter = {
  matches(toolName) { return toolName === 'Grep' || toolName === 'Glob' || toolName === 'Search' },
  parse(_, tc) {
    const i = inp(tc)
    const pattern = inputString(i, 'pattern', 'query', 'path', 'cmd') || firstLocation(tc) || titleAfterVerb(tc.title)
    return {
      header: { label: 'Search', detail: pattern.length > 70 ? pattern.slice(0, 67) + '…' : pattern },
      fileOps: [],
      effect: 'read',
    }
  }
}

const defaultAdapter: ToolAdapter = {
  matches() { return true },
  parse(toolName, tc) {
    const detail = tc.title && tc.title !== tc.kind ? tc.title : ''
    return {
      header: { label: toolName, detail },
      fileOps: [],
      effect: 'unknown',
    }
  }
}

// Order matters — more specific adapters first. bashNotebookAdapter must
// come before bashAdapter since both match on kind=execute.
const adapters: ToolAdapter[] = [
  codexApplyPatchAdapter,
  codexStdinAdapter,
  codexExecAdapter,
  notebookEditAdapter,
  bashNotebookAdapter,
  editAdapter,
  writeAdapter,
  readAdapter,
  bashAdapter,
  agentAdapter,
  webFetchAdapter,
  grepGlobAdapter,
  defaultAdapter,
]

export function parseTool(tc: RawToolCall): ParsedTool {
  const toolName = toolNameFor(tc)
  const i = inp(tc)
  for (const adapter of adapters) {
    if (adapter.matches(toolName, tc.kind, i)) {
      try {
        return adapter.parse(toolName, tc)
      } catch (err) {
        return {
          header: { label: toolName || 'Tool', detail: tc.title ? trunc(tc.title) : 'unparsed tool payload' },
          fileOps: [],
          effect: 'unknown',
          summaryOverride: err instanceof Error ? err.message : 'failed to parse tool payload',
        }
      }
    }
  }
  return defaultAdapter.parse(toolName, tc)
}

export const TOOL_HEADER_LABELS = [
  'Tool',
  'Read',
  'Search',
  'List',
  'Write',
  'Update',
  'Notebook',
  'Bash',
  'Run',
  'Git',
  'Agent',
  'Fetch',
  'Input',
  'Output',
] as const

function joinUniqueHeaderDetails(...parts: string[]): string {
  const out: string[] = []
  for (const part of parts.map(p => p.trim()).filter(Boolean)) {
    if (out.some(existing => existing === part || existing.includes(part) || part.includes(existing))) continue
    out.push(part)
  }
  return out.join(' · ')
}

export function normalizeToolHeader(label: string, detail: string): { label: string; detail: string } {
  const raw = label.trim()
  if (!raw) return { label: 'Tool', detail }

  const colonIdx = raw.indexOf(':')
  const beforeColon = colonIdx >= 0 ? raw.slice(0, colonIdx).trim() : raw
  const afterColon = colonIdx >= 0 ? raw.slice(colonIdx + 1).trim() : ''

  for (const known of TOOL_HEADER_LABELS) {
    if (beforeColon === known) {
      return { label: known, detail: detail || afterColon }
    }
    if (beforeColon.startsWith(`${known} `) || beforeColon.startsWith(`${known}\t`) || beforeColon.startsWith(`${known},`)) {
      const rest = beforeColon.slice(known.length).replace(/^[\s,]+/, '').trim()
      return { label: known, detail: joinUniqueHeaderDetails(rest, afterColon, detail) }
    }
  }

  return { label: beforeColon, detail: joinUniqueHeaderDetails(detail, afterColon) }
}

export function formatToolActivityText(tc: RawToolCall, maxDetail = 80): string {
  const parsed = parseTool(tc)
  const { label, detail } = normalizeToolHeader(parsed.header.label, parsed.header.detail)
  const fallback = parsed.summaryOverride || parsed.header.description || parsed.bashDescription || parsed.agentDescription || ''
  const chosen = detail || fallback
  if (!chosen) return label
  const trimmed = chosen.length > maxDetail ? `${chosen.slice(0, Math.max(0, maxDetail - 1))}…` : chosen
  return `${label}: ${trimmed}`
}

export function extractFileOpsFromEntries(entries: { kind: string; data?: any; ts?: number }[]): FileOp[] {
  const files: (FileOp & { ts?: number })[] = []
  const seen = new Map<string, number>()
  for (const e of entries) {
    if (e.kind !== 'tool') continue
    const parsed = parseTool(e.data as RawToolCall)
    for (const op of parsed.fileOps) {
      if (!op.path) continue
      const key = op.cellId ? `${op.path}#${op.cellId}` : op.path
      const idx = seen.get(key)
      if (idx !== undefined) {
        files[idx].verb = op.verb
        if (op.diffSummary) files[idx].diffSummary = op.diffSummary
        ;(files[idx] as any).ts = e.ts
      } else {
        seen.set(key, files.length)
        files.push({ ...op, ts: e.ts })
      }
    }
  }
  return files
}
