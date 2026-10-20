import { useRef, useState, useCallback, useEffect } from 'react'
import { AgentState } from '../types'
import {
  Entry,
  ContentBlock,
  ToolCall,
  SessionUpdate,
  PermissionOption,
  DeliveryState,
  textOf,
} from '../types/chat'
import { fetchTranscript, TranscriptEntry } from '../api'
import {
  BgShell,
  extractTaskId,
  isBackgroundSpawn,
  isAgentSpawn,
  isMonitorSpawn,
  extractBgLabel,
  followupTool,
  extractBashCommand,
  readToolText,
  parseBashOutputCompletion,
} from '../utils/bgShells'
import { isLocalCommandArtifact } from '../components/ChatTranscript'

// ─── Public types ──────────────────────────────────────────

export interface ChatConnectionState {
  entries: Entry[]
  bgShells: Map<string, BgShell>
  streamReady: boolean
  queuedMessages: string[]
  title: string
  reconfiguring: boolean
  debugLog: string[]
  wsBusy?: boolean
  busySince?: string | null
  compacting?: boolean
}

export interface ChatConnectionActions {
  sendPrompt: (text: string) => Promise<void>
  cancel: () => Promise<void>
  decidePermission: (requestId: number, optionId: string, cancelled: boolean) => Promise<void>
  reconnectSSE: () => void
  clearEntries: () => void
  appendSystemEntry: (text: string) => void
  retryMessage: (entry: Entry) => Promise<void>
  reloadTranscript: () => void
  flushQueue: () => void
  updateQueuedMessage: (index: number, text: string) => void
  removeQueuedMessage: (index: number) => void
  clearBgTasks: () => void
  forceIdle: () => Promise<void>
  respawnAcp: () => Promise<void>
}

// ─── JSON-RPC helpers ─────────────────────────────────────

interface JsonRpcNotification {
  jsonrpc: '2.0'
  method: string
  params?: unknown
}

interface JsonRpcRequest {
  jsonrpc: '2.0'
  id: number
  method: string
  params?: unknown
}

interface JsonRpcResponse {
  jsonrpc: '2.0'
  id: number
  result?: unknown
  error?: { code: number; message: string }
}

type JsonRpcFrame = JsonRpcNotification | JsonRpcRequest | JsonRpcResponse

function isRequest(frame: JsonRpcFrame): frame is JsonRpcRequest {
  return 'id' in frame && 'method' in frame && frame.method !== undefined
}

function isResponse(frame: JsonRpcFrame): frame is JsonRpcResponse {
  return 'id' in frame && !('method' in frame)
}

const OMITTED_EXEC_OUTPUT = '[exec output omitted by dashboard]'
const MAX_TOOL_TEXT = 4000
const TRANSCRIPT_TAIL = 200
const MAX_SEEDED_ENTRIES = 250

function truncateToolText(text: string): string {
  if (text.length <= MAX_TOOL_TEXT) return text
  return `${text.slice(0, MAX_TOOL_TEXT)}\n[output truncated by dashboard]`
}

function isCodexExecTool(tc: ToolCall): boolean {
  const raw = tc.rawInput
  return tc.title === 'exec_command' ||
    (!!raw && typeof raw === 'object' && typeof (raw as { cmd?: unknown }).cmd === 'string')
}

function compactToolCall(tc: ToolCall): ToolCall {
  const compacted: ToolCall = { ...tc }
  if (isCodexExecTool(tc)) {
    compacted.rawOutput = OMITTED_EXEC_OUTPUT
    compacted.content = [{ type: 'terminal', terminalOutput: OMITTED_EXEC_OUTPUT }]
    return compacted
  }

  if (typeof compacted.rawOutput === 'string') {
    compacted.rawOutput = truncateToolText(compacted.rawOutput)
  }
  if (Array.isArray(compacted.content)) {
    compacted.content = compacted.content.map((item: any) => {
      if (typeof item?.terminalOutput === 'string') {
        return { ...item, terminalOutput: truncateToolText(item.terminalOutput) }
      }
      if (typeof item?.text === 'string') {
        return { ...item, text: truncateToolText(item.text) }
      }
      if (item?.content?.type === 'text' && typeof item.content.text === 'string') {
        return { ...item, content: { ...item.content, text: truncateToolText(item.content.text) } }
      }
      return item
    })
  }
  return compacted
}

function isNotification(frame: JsonRpcFrame): frame is JsonRpcNotification {
  return 'method' in frame && !('id' in frame)
}

let rpcIdCounter = 1_000_000

function makeRequest(method: string, params?: unknown): JsonRpcRequest {
  return { jsonrpc: '2.0', id: rpcIdCounter++, method, params }
}

function makeNotification(method: string, params?: unknown): JsonRpcNotification {
  return { jsonrpc: '2.0', method, params }
}

function supportsCodexSlashCommands(agent?: AgentState): boolean {
  const backend = (agent?.agent_backend || '').toLowerCase()
  const model = (agent?.model || '').toLowerCase()
  return backend.includes('codex') ||
    backend.includes('gpt') ||
    model.includes('codex') ||
    model.includes('gpt')
}

function hasCompactionEntry(entries: Entry[]): boolean {
  return entries.some(e => e.kind === 'user' && e.text.trimStart().startsWith('Context compacted.'))
}

function entryMergeKey(e: Entry): string | null {
  switch (e.kind) {
    case 'user':
      return `user:${e.text.trim().slice(0, 500)}`
    case 'assistant':
      return `assistant:${(e.text || e.thoughts || '').trim().slice(0, 500)}`
    case 'tool':
      return e.data.toolCallId ? `tool:${e.data.toolCallId}` : null
    case 'system':
      return `system:${e.text.trim().slice(0, 300)}`
    default:
      return null
  }
}

function canReuseSeededEntryId(existing: Entry, seeded: Entry): boolean {
  if (existing.kind !== seeded.kind) return false

  switch (seeded.kind) {
    case 'assistant': {
      if (existing.kind !== 'assistant') return false
      const existingText = existing.text || ''
      const seededText = seeded.text || ''
      const existingThoughts = existing.thoughts || ''
      const seededThoughts = seeded.thoughts || ''
      // When a provider finishes a turn it may flush the native transcript and
      // replace the live SSE chunks with one complete assistant record. Preserve
      // the live row id if the transcript row is the same stream with more text;
      // otherwise React remounts TypewriterMarkdown and the final response snaps
      // in all at once.
      const textContinues = existingText !== '' && seededText.startsWith(existingText)
      const thoughtsContinue = existingThoughts !== '' && seededThoughts.startsWith(existingThoughts)
      return textContinues || thoughtsContinue || entryMergeKey(existing) === entryMergeKey(seeded)
    }
    case 'tool':
      return existing.kind === 'tool' && !!seeded.data.toolCallId && existing.data.toolCallId === seeded.data.toolCallId
    case 'user':
      return existing.kind === 'user' && existing.text.trim() === seeded.text.trim()
    case 'system':
      return existing.kind === 'system' && existing.text.trim() === seeded.text.trim()
    case 'permission':
      return existing.kind === 'permission' && existing.requestId === seeded.requestId
  }
  return false
}

function preserveLiveEntryIds(existing: Entry[], seeded: Entry[]): Entry[] {
  const used = new Set<number>()
  return seeded.map(seed => {
    const idx = existing.findIndex((entry, i) => !used.has(i) && canReuseSeededEntryId(entry, seed))
    if (idx < 0) return seed
    used.add(idx)
    const prev = existing[idx]
    return { ...seed, id: prev.id, ts: seed.ts || prev.ts } as Entry
  })
}

function entryKeySet(entries: Entry[]): Set<string> {
  return new Set(entries.map(entryMergeKey).filter(Boolean) as string[])
}

function nonDuplicateEntries(entries: Entry[], keys: Set<string>): Entry[] {
  return entries.filter(e => {
    if (e.kind === 'permission') return false
    const k = entryMergeKey(e)
    return !k || !keys.has(k)
  })
}

function replaceEntriesFromTranscript(conn: WsAgentInternals, seeded: Entry[]) {
  if (seeded.length === 0) return

  const existing = conn.entries
  const reconciledSeeded = preserveLiveEntryIds(conn.entries, seeded)
  let next = reconciledSeeded

  if (existing.length > 0) {
    const seededKeys = entryKeySet(reconciledSeeded)
    const overlapIdxs = existing
      .map((e, i) => {
        const k = entryMergeKey(e)
        return k && seededKeys.has(k) ? i : -1
      })
      .filter(i => i >= 0)

    if (overlapIdxs.length === 0) {
      // A native transcript can temporarily point at only the destination
      // backend (or only the newest turn) during provider switches / ACP
      // reconnects. Never replace a visible conversation with a disjoint
      // fragment; append the new transcript rows instead.
      next = [...nonDuplicateEntries(existing, seededKeys), ...reconciledSeeded]
    } else {
      // Treat the seeded transcript as authoritative for the overlapping
      // region, but preserve visible prefix/suffix rows that the native
      // transcript page did not include. This prevents "panel went blank" /
      // "history shrank to one response" races while still letting completed
      // transcript rows replace streaming chunks.
      const firstOverlap = overlapIdxs[0]
      const lastOverlap = overlapIdxs[overlapIdxs.length - 1]
      const prefix = nonDuplicateEntries(existing.slice(0, firstOverlap), seededKeys)
      const suffix = nonDuplicateEntries(existing.slice(lastOverlap + 1), seededKeys)
      next = [...prefix, ...reconciledSeeded, ...suffix]
    }
  }

  // Keep optimistic local prompts until the native transcript echoes them.
  const nextUserTexts = new Set(next.filter(e => e.kind === 'user').map(e => e.text.trim()))
  for (const e of conn.entries) {
    if (e.kind === 'user' && e.nonce && !nextUserTexts.has(e.text.trim())) {
      next = [...next, e]
    }
    if (e.kind === 'permission' && e.resolved === 'pending') {
      next = [...next, e]
    }
  }

  conn.entries = next.length > MAX_SEEDED_ENTRIES ? next.slice(-MAX_SEEDED_ENTRIES) : next
}

function makeResponse(id: number, result: unknown): JsonRpcResponse {
  return { jsonrpc: '2.0', id, result }
}

// ─── Per-agent mutable state ──────────────────────────────

interface WsAgentInternals {
  entries: Entry[]
  bgShells: Map<string, BgShell>
  streamReady: boolean
  queuedMessages: string[]
  title: string
  reconfiguring: boolean
  debugLog: string[]

  ws: WebSocket | null
  reconnectTimer: ReturnType<typeof setTimeout> | null
  reconnectCount: number
  lastKnownState: string
  sessionId: string
  pendingSpawn: Map<string, string>
  pendingCompactRequests: Set<number>
  bgCleanupTimer: ReturnType<typeof setTimeout> | null
  transcriptPollTimer: ReturnType<typeof setTimeout> | null
  reconfigure: { endsAt: number; pendingMsg: string } | null
  reconfigTimer: ReturnType<typeof setTimeout> | null
  busySince: string | null
}

function createWsInternals(sessionId: string): WsAgentInternals {
  return {
    entries: [],
    bgShells: new Map(),
    streamReady: false,
    queuedMessages: [],
    title: '',
    reconfiguring: false,
    debugLog: [],
    ws: null,
    reconnectTimer: null,
    reconnectCount: 0,
    lastKnownState: 'idle',
    sessionId,
    pendingSpawn: new Map(),
    pendingCompactRequests: new Set(),
    bgCleanupTimer: null,
    transcriptPollTimer: null,
    reconfigure: null,
    reconfigTimer: null,
    busySince: null,
  }
}

// ─── The hook ─────────────────────────────────────────────

export function useChatWebSockets(agents: AgentState[], activeChatId?: string | null): {
  getConnection: (pokegentId: string) => (ChatConnectionState & ChatConnectionActions) | null
} {
  const connectionsRef = useRef<Map<string, WsAgentInternals>>(new Map())
  const activeChatIdRef = useRef<string | null | undefined>(activeChatId)
  activeChatIdRef.current = activeChatId
  const [, forceRender] = useState(0)
  function notify() { forceRender(n => n + 1) }

  const addDebugLog = useCallback((pokegentId: string, msg: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    conn.debugLog = [...conn.debugLog.slice(-99), `${new Date().toLocaleTimeString()} ${msg}`]
  }, [])

  const setEntries = useCallback((pokegentId: string, updater: (prev: Entry[]) => Entry[]) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    conn.entries = updater(conn.entries)
    notify()
  }, [])

  const setBgShells = useCallback((pokegentId: string, updater: (prev: Map<string, BgShell>) => Map<string, BgShell>) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    conn.bgShells = updater(conn.bgShells)
    notify()
    scheduleBgCleanup(pokegentId)
  }, [])

  const appendEntry = useCallback((pokegentId: string, e: Entry) => {
    setEntries(pokegentId, prev => [...prev, { ...e, ts: e.ts || Date.now() }])
  }, [setEntries])

  const mergeTranscriptTools = useCallback((pokegentId: string, tail = 80) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn?.sessionId) return
    const requestedSessionId = conn.sessionId
    fetchTranscript(requestedSessionId, tail).then(page => {
      const conn = connectionsRef.current.get(pokegentId)
      if (!conn || conn.sessionId !== requestedSessionId) return
      const seeded = entriesFromTranscript(page.entries || []).filter((e): e is Extract<Entry, { kind: 'tool' }> => e.kind === 'tool')
      if (seeded.length === 0) return
      const existing = new Set(conn.entries
        .filter((e): e is Extract<Entry, { kind: 'tool' }> => e.kind === 'tool')
        .map(e => e.data.toolCallId || e.id))
      const missing = seeded.filter(e => !existing.has(e.data.toolCallId || e.id))
      if (missing.length === 0) return
      conn.entries = [...conn.entries, ...missing].sort((a, b) => (a.ts || 0) - (b.ts || 0))
      notify()
    }).catch(() => {})
  }, [])

  const scheduleTranscriptToolPoll = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn || conn.transcriptPollTimer || !conn.sessionId) return
    conn.transcriptPollTimer = setTimeout(() => {
      conn.transcriptPollTimer = null
      const cur = connectionsRef.current.get(pokegentId)
      if (!cur || cur.lastKnownState !== 'busy') return
      mergeTranscriptTools(pokegentId)
      scheduleTranscriptToolPoll(pokegentId)
    }, 2500)
  }, [mergeTranscriptTools])

  const stopTranscriptToolPoll = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    if (conn.transcriptPollTimer) {
      clearTimeout(conn.transcriptPollTimer)
      conn.transcriptPollTimer = null
    }
  }, [])

  const updateDeliveryState = useCallback((pokegentId: string, nonce: string, state: DeliveryState) => {
    setEntries(pokegentId, prev => prev.map(e =>
      e.kind === 'user' && e.nonce === nonce
        ? { ...e, deliveryState: state }
        : e
    ))
  }, [setEntries])

  // ── Background shell auto-cleanup ──

  const scheduleBgCleanup = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    if (conn.bgCleanupTimer) clearTimeout(conn.bgCleanupTimer)

    const stale: string[] = []
    conn.bgShells.forEach((s, id) => {
      if (s.status !== 'running' && s.endedAt && Date.now() - s.endedAt > 5000) {
        stale.push(id)
      }
    })
    if (stale.length > 0) {
      const next = new Map(conn.bgShells)
      stale.forEach(id => next.delete(id))
      conn.bgShells = next
      notify()
      return
    }
    let earliest = Infinity
    conn.bgShells.forEach(s => {
      if (s.status !== 'running' && s.endedAt) earliest = Math.min(earliest, s.endedAt)
    })
    if (earliest !== Infinity) {
      const wait = Math.max(100, 5000 - (Date.now() - earliest))
      conn.bgCleanupTimer = setTimeout(() => scheduleBgCleanup(pokegentId), wait)
    }
  }, [])

  // ── Reconfigure timeout ──

  const scheduleReconfigTimeout = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn || !conn.reconfiguring || !conn.reconfigure) return
    if (conn.reconfigTimer) clearTimeout(conn.reconfigTimer)
    const rc = conn.reconfigure
    const wait = Math.max(0, rc.endsAt - Date.now())
    conn.reconfigTimer = setTimeout(() => {
      const c = connectionsRef.current.get(pokegentId)
      if (!c || c.reconfigure !== rc) return
      c.reconfigure = null
      c.reconfiguring = false
      appendEntry(pokegentId, {
        kind: 'system',
        id: `e-${crypto.randomUUID()}`,
        text: 'Reconfigure timeout — agent did not reconnect. Try /cancel and re-issue.',
      })
    }, wait + 100)
  }, [appendEntry])

  // ── applyUpdate: the ACP event reducer ──

  const applyUpdate = useCallback((pokegentId: string, u: SessionUpdate) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return

    const next = [...conn.entries]
    const last = next[next.length - 1]
    const markBusy = () => {
      conn.lastKnownState = 'busy'
      conn.busySince = conn.busySince || new Date().toISOString()
      scheduleTranscriptToolPoll(pokegentId)
    }

    switch (u.sessionUpdate) {
      case 'agent_message_chunk': {
        markBusy()
        const t = textOf((u as { content: ContentBlock }).content)
        if (last && last.kind === 'assistant') {
          next[next.length - 1] = { ...last, text: last.text + t }
        } else {
          next.push({ kind: 'assistant', id: `a-${crypto.randomUUID()}`, text: t, thoughts: '', ts: Date.now() })
        }
        conn.entries = next
        notify()
        return
      }
      case 'agent_thought_chunk': {
        markBusy()
        const t = textOf((u as { content: ContentBlock }).content)
        if (last && last.kind === 'assistant') {
          next[next.length - 1] = { ...last, thoughts: last.thoughts + t }
        } else {
          next.push({ kind: 'assistant', id: `a-${crypto.randomUUID()}`, text: '', thoughts: t, ts: Date.now() })
        }
        conn.entries = next
        notify()
        return
      }
      case 'user_message_chunk': {
        markBusy()
        const echoNonce = (u as { nonce?: string }).nonce
        if (echoNonce && next.some(e => e.kind === 'user' && e.nonce === echoNonce)) return
        const ut = textOf((u as { content: ContentBlock }).content)
        if (last && last.kind === 'user' && last.text === ut) return
        next.push({ kind: 'user', id: `u-${crypto.randomUUID()}`, text: ut, ts: Date.now() })
        conn.entries = next
        notify()
        return
      }
      case 'tool_call': {
        markBusy()
        const tc = compactToolCall(u as unknown as ToolCall)
        if (isBackgroundSpawn(tc.kind, tc.rawInput)) {
          conn.pendingSpawn.set(tc.toolCallId, extractBashCommand(tc.rawInput))
        }
        if (isAgentSpawn(tc.title, tc.rawInput, tc.kind)) {
          const label = extractBgLabel(tc.title, tc.rawInput)
          if (!conn.bgShells.has(tc.toolCallId)) {
            const out = new Map(conn.bgShells)
            out.set(tc.toolCallId, { taskId: tc.toolCallId, command: label, startedAt: Date.now(), status: 'running', type: 'agent' })
            conn.bgShells = out
          }
        }
        if (isMonitorSpawn(tc.title, tc.kind)) {
          const label = extractBgLabel(tc.title, tc.rawInput)
          if (!conn.bgShells.has(tc.toolCallId)) {
            const out = new Map(conn.bgShells)
            out.set(tc.toolCallId, { taskId: tc.toolCallId, command: label, startedAt: Date.now(), status: 'running', type: 'monitor' })
            conn.bgShells = out
          }
        }
        next.push({ kind: 'tool', id: tc.toolCallId, data: tc, ts: Date.now() })
        conn.entries = next
        notify()
        scheduleBgCleanup(pokegentId)
        return
      }
      case 'tool_call_update': {
        markBusy()
        const tc = compactToolCall(u as unknown as ToolCall)
        if (!conn.pendingSpawn.has(tc.toolCallId) && isBackgroundSpawn(tc.kind, tc.rawInput)) {
          conn.pendingSpawn.set(tc.toolCallId, extractBashCommand(tc.rawInput))
        }
        if (!conn.bgShells.has(tc.toolCallId) && isAgentSpawn(tc.title, tc.rawInput, tc.kind)) {
          const label = extractBgLabel(tc.title, tc.rawInput)
          const out = new Map(conn.bgShells)
          out.set(tc.toolCallId, { taskId: tc.toolCallId, command: label, startedAt: Date.now(), status: 'running', type: 'agent' })
          conn.bgShells = out
        }
        if (!conn.bgShells.has(tc.toolCallId) && isMonitorSpawn(tc.title, tc.kind)) {
          const label = extractBgLabel(tc.title, tc.rawInput)
          const out = new Map(conn.bgShells)
          out.set(tc.toolCallId, { taskId: tc.toolCallId, command: label, startedAt: Date.now(), status: 'running', type: 'monitor' })
          conn.bgShells = out
        }
        const pendingCmd = conn.pendingSpawn.get(tc.toolCallId)
        if (pendingCmd !== undefined && tc.status === 'completed') {
          const taskId = extractTaskId(tc.rawOutput, tc.content) || tc.toolCallId
          conn.pendingSpawn.delete(tc.toolCallId)
          const cmd = pendingCmd || extractBashCommand(tc.rawInput)
          if (!conn.bgShells.has(taskId)) {
            const out = new Map(conn.bgShells)
            out.set(taskId, { taskId, command: cmd, startedAt: Date.now(), status: 'running' })
            conn.bgShells = out
          }
        }
        if ((tc.status === 'completed' || tc.status === 'failed') && conn.bgShells.has(tc.toolCallId)) {
          const text = readToolText(tc.content) || (typeof tc.rawOutput === 'string' ? tc.rawOutput : '')
          const cur = conn.bgShells.get(tc.toolCallId)
          if (cur && cur.status === 'running') {
            const out = new Map(conn.bgShells)
            out.set(tc.toolCallId, { ...cur, status: tc.status === 'completed' ? 'completed' : 'failed', endedAt: Date.now(), lastOutput: text ? text.slice(0, 200) : cur.lastOutput })
            conn.bgShells = out
          }
        }
        const followup = followupTool(tc.title, tc.kind)
        if (followup && tc.status === 'completed') {
          const targetTaskId = extractTaskId(tc.rawInput, tc.rawOutput, tc.content)
          if (targetTaskId) {
            const text = readToolText(tc.content) || (typeof tc.rawOutput === 'string' ? tc.rawOutput : '')
            if (followup === 'kill') {
              const cur = conn.bgShells.get(targetTaskId)
              if (cur) {
                const out = new Map(conn.bgShells)
                out.set(targetTaskId, { ...cur, status: 'killed', endedAt: Date.now(), lastOutput: text || cur.lastOutput })
                conn.bgShells = out
              }
            } else if (followup === 'output') {
              const completion = parseBashOutputCompletion(text)
              const cur = conn.bgShells.get(targetTaskId)
              const out = new Map(conn.bgShells)
              if (!cur) {
                out.set(targetTaskId, {
                  taskId: targetTaskId,
                  command: tc.title || 'background task',
                  startedAt: Date.now(),
                  status: completion ? completion.status : 'running',
                  endedAt: completion ? Date.now() : undefined,
                  exitCode: completion?.exitCode,
                  lastOutput: text,
                })
              } else {
                out.set(targetTaskId, {
                  ...cur,
                  status: completion ? completion.status : cur.status,
                  endedAt: completion ? Date.now() : cur.endedAt,
                  exitCode: completion?.exitCode ?? cur.exitCode,
                  lastOutput: text || cur.lastOutput,
                })
              }
              conn.bgShells = out
            }
          }
        }
        const idx = next.findIndex(e => e.kind === 'tool' && e.id === tc.toolCallId)
        if (idx >= 0) {
          const cur = next[idx] as Extract<Entry, { kind: 'tool' }>
          const merged = { ...cur.data } as Record<string, unknown>
          for (const [k, v] of Object.entries(tc)) {
            if (k === 'sessionUpdate') continue
            if (v === undefined || v === null) continue
            if (Array.isArray(v) && v.length === 0 && Array.isArray(merged[k]) && (merged[k] as unknown[]).length > 0) continue
            if (typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length === 0 && merged[k] != null && typeof merged[k] === 'object' && Object.keys(merged[k] as object).length > 0) continue
            merged[k] = v
          }
          next[idx] = { ...cur, data: merged as unknown as ToolCall }
        } else {
          next.push({ kind: 'tool', id: tc.toolCallId, data: tc, ts: Date.now() })
        }
        conn.entries = next
        notify()
        scheduleBgCleanup(pokegentId)
        return
      }
      case 'session_info_update': {
        const t = (u as { title?: string }).title
        if (typeof t === 'string') {
          conn.title = t
          notify()
        }
        return
      }
      default:
        return
    }
  }, [scheduleBgCleanup, scheduleTranscriptToolPoll])

  // ── WebSocket send helper ──

  const wsSend = useCallback((pokegentId: string, data: unknown): boolean => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn?.ws || conn.ws.readyState !== WebSocket.OPEN) return false
    conn.ws.send(JSON.stringify(data))
    return true
  }, [])

  // ── Queue drain: send one queued message if idle ──

  const drainOne = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn || (conn.lastKnownState !== 'idle' && conn.lastKnownState !== 'done') || conn.queuedMessages.length === 0) return
    const [next, ...rest] = conn.queuedMessages
    conn.queuedMessages = rest
    conn.lastKnownState = 'busy'
    notify()
    const nonce = crypto.randomUUID()
    appendEntry(pokegentId, { kind: 'user', id: `u-${crypto.randomUUID()}`, text: next, nonce, deliveryState: 'sending', ts: Date.now() })
    wsSend(pokegentId, makeRequest('session/prompt', {
      sessionId: conn.sessionId,
      prompt: [{ type: 'text', text: next }],
    }))
    updateDeliveryState(pokegentId, nonce, 'confirmed')
  }, [appendEntry, updateDeliveryState, wsSend])

  // ── Route a raw JSON-RPC frame from the WebSocket ──

  const handleFrame = useCallback((pokegentId: string, frame: JsonRpcFrame) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return

    // JSON-RPC request from ACP (e.g. session/request_permission)
    if (isRequest(frame)) {
      if (frame.method === 'session/request_permission') {
        const params = frame.params as {
          toolCall?: ToolCall
          options?: PermissionOption[]
        } | undefined
        const requestId = frame.id
        const options = params?.options || []
        setEntries(pokegentId, prev => {
          if (prev.some(e => e.kind === 'permission' && e.requestId === requestId)) return prev
          return [...prev, {
            kind: 'permission',
            id: `perm-${requestId}`,
            requestId,
            toolCall: params?.toolCall,
            options,
            resolved: 'pending',
          }]
        })
      }
      return
    }

    // JSON-RPC response to dashboard-originated requests.
    if (isResponse(frame)) {
      if (conn.pendingCompactRequests.has(frame.id)) {
        conn.pendingCompactRequests.delete(frame.id)
        if (frame.error) {
          appendEntry(pokegentId, {
            kind: 'system',
            id: `s-${crypto.randomUUID()}`,
            text: `Compaction failed: ${frame.error.message || 'unknown error'}`,
          })
        } else if (conn.sessionId) {
          const requestedSessionId = conn.sessionId
          fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
            const current = connectionsRef.current.get(pokegentId)
            if (!current || current.sessionId !== requestedSessionId) return
            const seeded = entriesFromTranscript(page.entries || [])
            if (seeded.length > 0) {
              replaceEntriesFromTranscript(current, seeded)
            }
            if (!hasCompactionEntry(current.entries)) {
              current.entries = [...current.entries, {
                kind: 'user',
                id: `compact-${crypto.randomUUID()}`,
                text: 'Context compacted.',
                ts: Date.now(),
                deliveryState: 'confirmed',
              }]
            }
            notify()
          }).catch(() => {
            appendEntry(pokegentId, {
              kind: 'user',
              id: `compact-${crypto.randomUUID()}`,
              text: 'Context compacted.',
              deliveryState: 'confirmed',
            })
          })
        } else if (!hasCompactionEntry(conn.entries)) {
          appendEntry(pokegentId, {
            kind: 'user',
            id: `compact-${crypto.randomUUID()}`,
            text: 'Context compacted.',
            deliveryState: 'confirmed',
          })
        }
        notify()
      }
      return
    }

    // JSON-RPC notification from ACP
    if (isNotification(frame)) {
      const params = frame.params as Record<string, unknown> | undefined

      if (frame.method === 'session/update') {
        const update = params?.update as SessionUpdate | undefined
        if (update) {
          applyUpdate(pokegentId, update)
        }
        return
      }

      if (frame.method === 'claude/session_state') {
        const raw = (params?.state as string) || 'idle'
        const state = (raw === 'running' || raw === 'processing' || raw === 'waiting' || raw === 'busy') ? 'busy' : raw === 'done' ? 'done' : 'idle'
        conn.lastKnownState = state
        conn.busySince = state === 'busy' ? (conn.busySince || new Date().toISOString()) : null
        if (state === 'busy') scheduleTranscriptToolPoll(pokegentId)
        else {
          stopTranscriptToolPoll(pokegentId)
          mergeTranscriptTools(pokegentId)
        }
        notify()
        if (state === 'idle' || state === 'done') drainOne(pokegentId)
        return
      }

      if (frame.method === 'pokegents/system_message') {
        const text = (params?.text as string) || ''
        if (text) {
          appendEntry(pokegentId, { kind: 'system', id: `sys-${crypto.randomUUID()}`, text })
        }
        return
      }

      if (frame.method === 'pokegents/refetch_transcript') {
        if (conn.sessionId) {
          const requestedSessionId = conn.sessionId
          fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
            const current = connectionsRef.current.get(pokegentId)
            if (!current || current.sessionId !== requestedSessionId) return
            const seeded = entriesFromTranscript(page.entries || [])
            if (seeded.length > 0) {
              replaceEntriesFromTranscript(current, seeded)
              notify()
            }
          }).catch(() => {})
        }
        return
      }

      if (frame.method === 'claude/task_started') {
        const d = params as { taskId: string; description?: string; workflowName?: string; taskType?: string } | undefined
        if (!d?.taskId) return
        setBgShells(pokegentId, prev => {
          if (prev.has(d.taskId)) return prev
          const out = new Map(prev)
          out.set(d.taskId, { taskId: d.taskId, command: d.description || d.workflowName || 'background task', startedAt: Date.now(), status: 'running', type: d.taskType === 'local_bash' ? 'shell' : 'agent' })
          return out
        })
        return
      }

      if (frame.method === 'claude/task_notification') {
        const d = params as { taskId: string; status: string; summary?: string } | undefined
        if (!d?.taskId) return
        setBgShells(pokegentId, prev => {
          const cur = prev.get(d.taskId)
          if (!cur) return prev
          const out = new Map(prev)
          const status = d.status === 'completed' ? 'completed' : d.status === 'stopped' ? 'killed' : 'failed'
          out.set(d.taskId, { ...cur, status, endedAt: Date.now(), lastOutput: d.summary } as BgShell)
          return out
        })
        return
      }

      if (frame.method === 'claude/task_progress') {
        const d = params as { taskId: string; summary?: string; description?: string } | undefined
        if (!d?.taskId) return
        setBgShells(pokegentId, prev => {
          const cur = prev.get(d.taskId)
          if (!cur) return prev
          const out = new Map(prev)
          out.set(d.taskId, { ...cur, lastOutput: d.summary || d.description })
          return out
        })
        return
      }

      if (frame.method === 'claude/api_retry') {
        const d = params as { attempt: number; maxRetries: number; error?: string; retryDelayMs?: number } | undefined
        if (d) {
          appendEntry(pokegentId, { kind: 'system', id: `s-${crypto.randomUUID()}`, text: `API retry ${d.attempt}/${d.maxRetries} (${d.error || 'unknown'}) — waiting ${Math.round((d.retryDelayMs || 0) / 1000)}s` })
        }
        return
      }

      if (frame.method === 'claude/rate_limit') {
        const d = params as { rateLimitInfo?: { status?: string; utilization?: number; resetsAt?: string } } | undefined
        const info = d?.rateLimitInfo
        if (info?.status === 'rejected' || info?.status === 'allowed_warning') {
          const pct = info.utilization != null ? `${Math.round(info.utilization * 100)}%` : ''
          let resetStr = ''
          if (info.resetsAt) {
            const resetsMs = new Date(info.resetsAt).getTime() - Date.now()
            resetStr = resetsMs > 0 ? ` — resets in ${Math.ceil(resetsMs / 60000)}m` : ' — resets soon'
          }
          appendEntry(pokegentId, { kind: 'system', id: `s-${crypto.randomUUID()}`, text: `Rate limit ${info.status === 'rejected' ? 'hit' : 'warning'} ${pct}${resetStr}` })
        }
        return
      }
    }
  }, [applyUpdate, appendEntry, setEntries, setBgShells, updateDeliveryState, wsSend, drainOne, mergeTranscriptTools, scheduleTranscriptToolPoll, stopTranscriptToolPoll])

  const handleFrameRef = useRef(handleFrame)
  handleFrameRef.current = handleFrame

  // ── WebSocket connection management per agent ──

  const connectWs = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    if (conn.ws && conn.ws.readyState <= WebSocket.OPEN) return

    if (conn.reconnectTimer) { clearTimeout(conn.reconnectTimer); conn.reconnectTimer = null }

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}/api/chat/${pokegentId}/ws`
    const ws = new WebSocket(url)
    conn.ws = ws

    ws.onopen = () => {
      conn.reconnectCount = 0
      addDebugLog(pokegentId, 'ws: connected')
    }

    ws.onmessage = (event) => {
      let data: unknown
      try { data = JSON.parse(event.data as string) } catch { return }

      // Bootstrap frame from the Go server
      if ((data as { type?: string }).type === 'ws_connected') {
        const bootstrap = data as { session_id?: string; state?: string }
        if (bootstrap.session_id) conn.sessionId = bootstrap.session_id
        conn.lastKnownState = bootstrap.state || 'idle'
        conn.streamReady = true
        notify()
        addDebugLog(pokegentId, `ws: bootstrap session=${bootstrap.session_id?.slice(0, 8)} state=${bootstrap.state}`)
        return
      }

      handleFrameRef.current(pokegentId, data as JsonRpcFrame)
    }

    ws.onclose = () => {
      conn.ws = null
      conn.streamReady = false
      notify()
      addDebugLog(pokegentId, 'ws: disconnected')
      scheduleReconnect(pokegentId)
    }

    ws.onerror = () => {
      // onclose will fire after onerror
      addDebugLog(pokegentId, 'ws: error')
    }
  }, [addDebugLog])

  const scheduleReconnect = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    if (conn.reconnectTimer) return

    const delay = Math.min(1000 * Math.pow(2, conn.reconnectCount), 10000)
    conn.reconnectCount++

    conn.reconnectTimer = setTimeout(() => {
      conn.reconnectTimer = null
      const c = connectionsRef.current.get(pokegentId)
      if (!c) return

      // Reseed from transcript before reconnecting
      if (c.sessionId) {
        const requestedSessionId = c.sessionId
        fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
          const seeded = entriesFromTranscript(page.entries || [])
          const cur = connectionsRef.current.get(pokegentId)
          if (cur && cur.sessionId === requestedSessionId && seeded.length > 0) {
            replaceEntriesFromTranscript(cur, seeded)
            notify()
          }
        }).catch(() => {})
      }

      connectWs(pokegentId)
    }, delay)
  }, [connectWs])

  const disconnectWs = useCallback((pokegentId: string) => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return
    if (conn.reconnectTimer) { clearTimeout(conn.reconnectTimer); conn.reconnectTimer = null }
    if (conn.ws) {
      conn.ws.onclose = null
      conn.ws.close()
      conn.ws = null
    }
    conn.streamReady = false
  }, [])

  // ── Lifecycle: sync connections with agents list ──

  const agentsRef = useRef(agents)
  agentsRef.current = agents

  const connectWsRef = useRef(connectWs)
  connectWsRef.current = connectWs
  const disconnectWsRef = useRef(disconnectWs)
  disconnectWsRef.current = disconnectWs

  const syncRef = useRef(() => {})
  syncRef.current = () => {
    const activeId = activeChatIdRef.current
    const chatAgents = agentsRef.current.filter(a => a.interface === 'chat')
    const chatIds = new Set(chatAgents.map(a => a.pokegent_id || a.session_id))

    for (const agent of chatAgents) {
      const id = agent.pokegent_id || agent.session_id
      if (!connectionsRef.current.has(id)) {
        const internals = createWsInternals(agent.session_id)
        connectionsRef.current.set(id, internals)

        // Initial transcript backfill is intentionally lazy. Keeping 1000-entry
        // histories for every chat agent was expensive in the Chrome
        // renderer; seed only the open panel or currently busy agents.
        if (id === activeChatIdRef.current || agent.state === 'busy') {
          const requestedSessionId = agent.session_id
          fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
            const seeded = entriesFromTranscript(page.entries || [])
            const conn = connectionsRef.current.get(id)
            if (conn && conn.sessionId === requestedSessionId && seeded.length > 0 && conn.entries.length === 0) {
              replaceEntriesFromTranscript(conn, seeded)
              notify()
            }
          }).catch(() => {})
        }

        connectWsRef.current(id)
      } else {
        const conn = connectionsRef.current.get(id)
        if (conn && agent.session_id && conn.sessionId !== agent.session_id) {
          const oldShort = conn.sessionId?.slice(0, 8) || 'unknown'
          conn.sessionId = agent.session_id
          conn.bgShells = new Map()
          addDebugLog(id, `session changed: ${oldShort} → ${agent.session_id.slice(0, 8)}`)
          disconnectWsRef.current(id)
          connectWsRef.current(id)
          if (id === activeChatIdRef.current || agent.state === 'busy') {
            const requestedSessionId = agent.session_id
            fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
              const seeded = entriesFromTranscript(page.entries || [])
              const current = connectionsRef.current.get(id)
              if (current && current.sessionId === requestedSessionId && seeded.length > 0) {
                replaceEntriesFromTranscript(current, seeded)
                notify()
              }
            }).catch(() => {})
          }
          notify()
        }
      }
    }

    for (const [id, conn] of Array.from(connectionsRef.current.entries())) {
      if (!chatIds.has(id)) {
        disconnectWsRef.current(id)
        if (conn.bgCleanupTimer) clearTimeout(conn.bgCleanupTimer)
        if (conn.transcriptPollTimer) clearTimeout(conn.transcriptPollTimer)
        if (conn.reconfigTimer) clearTimeout(conn.reconfigTimer)
        connectionsRef.current.delete(id)
      }
    }
  }

  useEffect(() => {
    syncRef.current()
    const iv = setInterval(() => syncRef.current(), 2000)
    return () => clearInterval(iv)
  }, [])

  useEffect(() => { syncRef.current() }, [agents])
  useEffect(() => { syncRef.current() }, [activeChatId])

  // Load full transcript history only when a chat panel is actually opened.
  useEffect(() => {
    if (!activeChatId) return
    const conn = connectionsRef.current.get(activeChatId)
    if (!conn || conn.entries.length > 0 || !conn.sessionId) return
    const requestedSessionId = conn.sessionId
    fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
      const seeded = entriesFromTranscript(page.entries || [])
      const current = connectionsRef.current.get(activeChatId)
      if (current && current.sessionId === requestedSessionId && seeded.length > 0 && current.entries.length === 0) {
        replaceEntriesFromTranscript(current, seeded)
        notify()
      }
    }).catch(() => {})
  }, [activeChatId])

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      for (const [id, conn] of connectionsRef.current.entries()) {
        disconnectWsRef.current(id)
        if (conn.bgCleanupTimer) clearTimeout(conn.bgCleanupTimer)
        if (conn.reconfigTimer) clearTimeout(conn.reconfigTimer)
      }
      connectionsRef.current.clear()
    }
  }, [])

  // ── Build actions for a given agent ──

  const getConnection = useCallback((pokegentId: string): (ChatConnectionState & ChatConnectionActions) | null => {
    const conn = connectionsRef.current.get(pokegentId)
    if (!conn) return null

    const isBusy = conn.lastKnownState === 'busy'
    const agent = agents.find(a => (a.pokegent_id || a.session_id) === pokegentId)
    const appendCommandEntry = (commandText: string) => {
      appendEntry(pokegentId, {
        kind: 'user',
        id: `u-${crypto.randomUUID()}`,
        text: commandText,
        deliveryState: 'confirmed',
      })
    }

    const sendPrompt = async (text: string) => {
      // Dashboard-level slash commands
      if (text.startsWith('/')) {
        const head = text.split(/\s+/, 1)[0]
        switch (head) {
          case '/cancel':
            appendCommandEntry(text)
            await cancelAgent()
            appendEntry(pokegentId, { kind: 'system', id: `s-${crypto.randomUUID()}`, text: 'Cancelled current turn.' })
            return
          case '/clear':
            conn.entries = []
            appendCommandEntry(text)
            notify()
            return
          case '/exit':
            appendCommandEntry(text)
            await fetch(`/api/sessions/${pokegentId}/shutdown`, { method: 'POST' })
            appendEntry(pokegentId, { kind: 'system', id: `s-${crypto.randomUUID()}`, text: 'Shutdown signal sent.' })
            return
          case '/compact': {
            appendCommandEntry(text)
            if (!supportsCodexSlashCommands(agent)) {
              appendEntry(pokegentId, {
                kind: 'system',
                id: `s-${crypto.randomUUID()}`,
                text:
                  '/compact is available for Codex chat agents. ' +
                  'For Claude agents, switch to iTerm2 mode to use Claude CLI slash commands.',
              })
              return
            }
            if (isBusy) {
              appendEntry(pokegentId, {
                kind: 'system',
                id: `s-${crypto.randomUUID()}`,
                text: 'Wait for the current turn to finish before compacting.',
              })
              return
            }
            const req = makeRequest('session/prompt', {
              sessionId: conn.sessionId,
              prompt: [{ type: 'text', text: '/compact' }],
            })
            conn.pendingCompactRequests.add(req.id)
            const sent = wsSend(pokegentId, req)
            if (sent) {
              conn.lastKnownState = 'busy'
              conn.busySince = new Date().toISOString()
              scheduleTranscriptToolPoll(pokegentId)
              notify()
            } else {
              conn.pendingCompactRequests.delete(req.id)
              appendEntry(pokegentId, { kind: 'system', id: `e-${crypto.randomUUID()}`, text: 'Error: WebSocket not connected' })
            }
            return
          }
          case '/model':
          case '/effort': {
            appendCommandEntry(text)
            const arg = text.slice(head.length).trim()
            if (!arg) {
              appendEntry(pokegentId, {
                kind: 'system',
                id: `s-${crypto.randomUUID()}`,
                text: head === '/model'
                  ? 'Usage: /model <name>\nShortcuts: opus, sonnet, haiku (resolves to latest version)'
                  : 'Usage: /effort <level>  (low / medium / high / max)',
              })
              return
            }
            if (head === '/effort' && !['low', 'medium', 'high', 'max'].includes(arg)) {
              appendEntry(pokegentId, {
                kind: 'system',
                id: `s-${crypto.randomUUID()}`,
                text: `/effort must be one of: low, medium, high, max — got "${arg}".`,
              })
              return
            }
            conn.reconfigure = {
              endsAt: Date.now() + 20000,
              pendingMsg: head === '/model'
                ? `Switching model → ${arg}…`
                : `Set thinking effort → ${arg}.`,
            }
            conn.reconfiguring = true
            notify()
            scheduleReconfigTimeout(pokegentId)
            try {
              const r = await fetch(`/api/sessions/${pokegentId}/runtime-config`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(head === '/model' ? { model: arg } : { effort: arg }),
              })
              if (!r.ok) {
                const msg = await r.text()
                conn.reconfigure = null
                conn.reconfiguring = false
                notify()
                appendEntry(pokegentId, { kind: 'system', id: `e-${crypto.randomUUID()}`, text: `Error: ${msg}` })
              } else {
                const result = await r.json()
                if (head === '/model' && result.model && result.model !== arg) {
                  conn.reconfigure = {
                    endsAt: conn.reconfigure?.endsAt ?? Date.now() + 20000,
                    pendingMsg: `Switched model → ${result.model} (from "${arg}").`,
                  }
                  notify()
                }
              }
            } catch (err) {
              conn.reconfigure = null
              conn.reconfiguring = false
              notify()
              appendEntry(pokegentId, { kind: 'system', id: `e-${crypto.randomUUID()}`, text: `Error: ${String(err)}` })
            }
            return
          }
          case '/help':
            appendCommandEntry(text)
            appendEntry(pokegentId, {
              kind: 'system',
              id: `s-${crypto.randomUUID()}`,
              text:
                'Chat-mode commands: /cancel, /clear (local only), /exit, /compact (Codex), /model <name>, /effort <low|medium|high|max>, /help.\n' +
                '/model and /effort restart the chat session under the hood — same Claude session_id is preserved.\n' +
                'Other Claude CLI commands like /cost and /memory still need iTerm2 mode.',
            })
            return
          default:
            appendCommandEntry(text)
            appendEntry(pokegentId, {
              kind: 'system',
              id: `s-${crypto.randomUUID()}`,
              text:
                `${head} is a Claude CLI command and is not available in chat mode. ` +
                'Switch this agent to iTerm2 (right-click card → Switch to iTerm2) to use it. ' +
                'Type /help to see chat-mode commands.',
            })
            return
        }
      }

      if (isBusy) {
        conn.queuedMessages = [...conn.queuedMessages, text]
        notify()
        return
      }

      const nonce = crypto.randomUUID()
      appendEntry(pokegentId, { kind: 'user', id: `u-${crypto.randomUUID()}`, text, nonce, deliveryState: 'sending' })
      const sent = wsSend(pokegentId, makeRequest('session/prompt', {
        sessionId: conn.sessionId,
        prompt: [{ type: 'text', text }],
      }))
      if (sent) {
        conn.lastKnownState = 'busy'
        conn.busySince = new Date().toISOString()
        updateDeliveryState(pokegentId, nonce, 'confirmed')
      } else {
        updateDeliveryState(pokegentId, nonce, 'failed')
        appendEntry(pokegentId, { kind: 'system', id: `e-${crypto.randomUUID()}`, text: 'Error: WebSocket not connected' })
      }
    }

    const cancelAgent = async () => {
      // Promote in-progress bash tool calls to background shells
      const entries = conn.entries
      for (let i = entries.length - 1; i >= 0; i--) {
        const e = entries[i]
        if (e.kind === 'tool' && e.data.kind === 'execute' && e.data.status !== 'completed' && e.data.status !== 'failed') {
          const cmd = extractBashCommand(e.data.rawInput) || e.data.title || 'background task'
          const taskId = e.data.toolCallId
          if (!conn.bgShells.has(taskId)) {
            const out = new Map(conn.bgShells)
            out.set(taskId, { taskId, command: cmd, startedAt: Date.now(), status: 'running' })
            conn.bgShells = out
          }
          break
        }
      }
      wsSend(pokegentId, makeNotification('session/cancel', { sessionId: conn.sessionId }))
      fetch(`/api/sessions/${pokegentId}/cancel`, { method: 'POST' })
        .then(() => {
          conn.lastKnownState = 'idle'
          conn.busySince = null
          notify()
          drainOne(pokegentId)
        })
        .catch(() => {})
    }

    const decidePermission = async (requestId: number, optionId: string, cancelled: boolean) => {
      setEntries(pokegentId, prev => prev.map(e =>
        e.kind === 'permission' && e.requestId === requestId
          ? { ...e, resolved: cancelled ? 'denied' : (optionId.startsWith('reject') ? 'denied' : 'allowed') }
          : e
      ))
      const result = cancelled
        ? { outcome: { outcome: 'cancelled' } }
        : { outcome: { outcome: 'selected', optionId } }
      const sent = wsSend(pokegentId, makeResponse(requestId, result))
      if (!sent) {
        // Revert on failure
        setEntries(pokegentId, prev => prev.map(e =>
          e.kind === 'permission' && e.requestId === requestId ? { ...e, resolved: 'pending' } : e
        ))
      }
    }

    const reconnectSSE = () => {
      // In WS mode, reconnect the WebSocket
      disconnectWs(pokegentId)
      connectWs(pokegentId)
    }

    const clearEntries = () => {
      conn.entries = []
      notify()
    }

    const appendSystemEntry = (text: string) => {
      appendEntry(pokegentId, { kind: 'system', id: `s-${crypto.randomUUID()}`, text })
    }

    const retryMessage = async (entry: Entry) => {
      if (entry.kind !== 'user' || !entry.nonce) return
      updateDeliveryState(pokegentId, entry.nonce, 'sending')
      const sent = wsSend(pokegentId, makeRequest('session/prompt', { prompt: entry.text, nonce: entry.nonce }))
      updateDeliveryState(pokegentId, entry.nonce, sent ? 'confirmed' : 'failed')
    }

    const reloadTranscript = () => {
      if (!conn.sessionId) return
      addDebugLog(pokegentId, 'action: reload transcript')
      const requestedSessionId = conn.sessionId
      fetchTranscript(requestedSessionId, TRANSCRIPT_TAIL).then(page => {
        if (conn.sessionId !== requestedSessionId) return
        const seeded = entriesFromTranscript(page.entries || [])
        if (seeded.length > 0) {
          replaceEntriesFromTranscript(conn, seeded)
          notify()
        }
        addDebugLog(pokegentId, `transcript: ${seeded.length} entries loaded`)
      }).catch(() => {})
    }

    const flushQueue = () => {
      conn.queuedMessages = []
      notify()
    }

    const updateQueuedMessage = (index: number, text: string) => {
      if (index < 0 || index >= conn.queuedMessages.length) return
      const next = [...conn.queuedMessages]
      const trimmed = text.trim()
      if (trimmed) next[index] = text
      else next.splice(index, 1)
      conn.queuedMessages = next
      notify()
    }

    const removeQueuedMessage = (index: number) => {
      if (index < 0 || index >= conn.queuedMessages.length) return
      conn.queuedMessages = conn.queuedMessages.filter((_, i) => i !== index)
      notify()
    }

    const clearBgTasks = () => {
      addDebugLog(pokegentId, 'action: cleared bg tasks')
      conn.bgShells = new Map()
      notify()
    }

    const forceIdle = async () => {
      await fetch(`/api/sessions/${pokegentId}/debug/force-idle`, { method: 'POST' })
      addDebugLog(pokegentId, 'action: force idle')
    }

    const respawnAcp = async () => {
      addDebugLog(pokegentId, 'action: respawn ACP')
      await fetch(`/api/sessions/${pokegentId}/restart-backend`, { method: 'POST' })
    }

    return {
      entries: conn.entries,
      bgShells: conn.bgShells,
      streamReady: conn.streamReady,
      wsBusy: conn.lastKnownState === 'busy',
      busySince: conn.busySince,
      compacting: conn.pendingCompactRequests.size > 0,
      queuedMessages: conn.queuedMessages,
      title: conn.title,
      reconfiguring: conn.reconfiguring,
      debugLog: conn.debugLog,
      sendPrompt,
      cancel: cancelAgent,
      decidePermission,
      reconnectSSE,
      clearEntries,
      appendSystemEntry,
      retryMessage,
      reloadTranscript,
      flushQueue,
      updateQueuedMessage,
      removeQueuedMessage,
      clearBgTasks,
      forceIdle,
      respawnAcp,
    }
  }, [agents, appendEntry, updateDeliveryState, setEntries, setBgShells, addDebugLog, wsSend, connectWs, disconnectWs, scheduleReconfigTimeout])

  return { getConnection }
}

// ─── entriesFromTranscript ─────────────────────────────────

function entriesFromTranscript(transcript: TranscriptEntry[]): Entry[] {
  const out: Entry[] = []

  const toolResults = new Map<string, string>()
  for (const t of transcript) {
    if (t.type === 'tool_result' && t.tool_use_id && t.content) {
      toolResults.set(t.tool_use_id, t.content)
    }
  }

  for (const t of transcript) {
    const ts = t.timestamp ? new Date(t.timestamp).getTime() : undefined
    if (t.type === 'user' && t.content) {
      if (isLocalCommandArtifact(t.content)) continue
      out.push({ kind: 'user', id: `seed-u-${crypto.randomUUID()}`, text: t.content, ts })
      continue
    }
    if (t.type === 'assistant') {
      const blocks = t.blocks || []
      let text = ''
      let thoughts = ''
      for (const b of blocks) {
        if (b.type === 'text' && b.text) text += b.text
        else if (b.type === 'thinking' && b.text) thoughts += b.text
      }
      if (text || thoughts) out.push({ kind: 'assistant', id: `seed-a-${crypto.randomUUID()}`, text, thoughts, ts })
      for (const b of blocks) {
        if (b.type === 'tool_use' && b.name) {
          const toolId = b.id || ''
          let title = b.name
          let rawInput: unknown = undefined
          try {
            const inputStr = b.input || '{}'
            const parsed = JSON.parse(inputStr)
            rawInput = parsed
            if (parsed.command) title = `${b.name}: ${parsed.command.slice(0, 60)}`
            else if (parsed.file_path) title = `${b.name}: ${parsed.file_path}`
            else if (parsed.pattern) title = `${b.name}: ${parsed.pattern}`
          } catch {
            if (b.input && b.input.length > 2) rawInput = b.input
          }
          const result = toolResults.get(toolId)
          out.push({
            kind: 'tool',
            id: `seed-t-${crypto.randomUUID()}`,
            ts,
            data: compactToolCall({
              toolCallId: toolId || `seed-tool-${crypto.randomUUID()}`,
              title,
              kind: b.name,
              status: 'completed',
              rawInput: rawInput && Object.keys(rawInput as object).length > 0 ? rawInput : undefined,
              rawOutput: result || undefined,
            }),
          })
        }
      }
    }
  }
  return out.length > MAX_SEEDED_ENTRIES ? out.slice(-MAX_SEEDED_ENTRIES) : out
}
