export interface Capabilities {
  app_version: string
  schema_version: string | number
  runtime_version: string
  runtime_path: string
  chat_enabled: boolean
  disabled_reason: string
  local_preview_assets?: boolean
  desktop_enabled?: boolean
  terminal_available?: boolean
}

export type McpScope = 'read' | 'task-write'

export interface McpSettings {
  supported: boolean
  enabled: boolean
  scope: McpScope
  connection_path: string
  executable_path: string
  registration_prompt: string
  tools: string[]
}

export interface McpSettingsUpdate {
  request_id: string
  enabled: boolean
  scope: McpScope
  rotate?: boolean
}

export interface HistoryInfo {
  available: boolean
  executable_path: string
  home_path: string
  runtime_version: string
  reason: string
}
export interface HistoryItem {
  id: string
  title: string
  preview: string
  project_path: string
  created_at: number
  updated_at: number
  source: string
  status: string
}
export interface HistoryMessage { key: string; role: string; body: string; at: string; time_source?: string }
export interface HistorySkipped { position: string; reason: string; kind: string }
export interface HistoryPreview {
  thread: HistoryItem
  sha256: string
  messages: HistoryMessage[]
  skipped: HistorySkipped[]
  partial: boolean
}
export interface HistoryImportResult {
  task_id: string
  source_id: string
  imported: number
  duplicates: number
  skipped: HistorySkipped[]
  partial: boolean
}

export interface Project {
  id: string
  name: string
  display_path: string
  resolved_path: string
  archived: boolean
}

export interface Role {
  id: string
  name: string
  prompt: string
}

export interface AcceptanceCriterion {
  criterion_id: string
  procedure: string
  expected: string
  required: boolean
}

export interface TaskSpec {
  project_id: string
  role_id?: string
  objective: string
  acceptance_criteria: AcceptanceCriterion[]
  allowed_scope: string
  out_of_scope: string
  approval_profile: 'read-only' | 'workspace-write'
  version: number
}

export interface Task {
  id: string
  spec: TaskSpec
  state: string
  version: number
  current_run_id?: string | null
  submission_id?: string | null
  block?: unknown
}

export interface Run {
  id: string
  task_id: string
  spec_version: number
  generation: number
  state: string
  provider_thread_id: string | null
  provider_turn_id: string | null
  started_at: string | null
  ended_at: string | null
  error: unknown
  spec_snapshot: TaskSpec
  role_prompt: string
  runtime_version: string
  model: string
  effective_policy: string
  workspace_quiet: boolean
}

export interface Approval {
  id: string
  run_id: string
  generation: number
  kind: string
  request: unknown
  state: string
  choices: Array<'accept' | 'decline'>
}

export interface Notification {
  id: string
  task_id: string
  kind: string
  read: boolean
  created_at: string
}

export interface ArtifactRef {
  path_or_uri: string
  revision_or_hash: string
  description: string
}

export interface Evidence {
  criterion_id: string
  revision: string
  procedure: string
  result: string
  passed: boolean
}

export interface AgentProfile { name: string; sprite: string }

export interface Snapshot {
  settings?: { locale: 'ja-JP' | 'en-US'; board_order?: string[]; agent_profiles?: Record<string, AgentProfile>; desktop?: { notifications: boolean; background: boolean } }
  previews?: Record<string, Pick<Message, 'body' | 'role' | 'created_at'>>
  review_decisions?: unknown[]
  recovery?: unknown[]
  projects: Project[]
  roles: Role[]
  tasks: Task[]
  runs: Run[]
  approvals: Approval[]
  submissions: Submission[]
  contexts: ContextPacket[]
  effects: Effect[]
  notifications: Notification[]
  event_seq: number
}

export interface Message {
  id: string
  task_id: string
  run_id: string
  role: string
  body: string
  created_at: string
}

export interface Page<T> {
  items: T[]
  next_cursor: string | null
}

export interface Submission {
  submission_id: string
  task_id: string
  run_id: string
  spec_version: number
  summary: string
  criterion_evidence: Evidence[]
  artifact_refs: ArtifactRef[]
  known_limitations: string[]
}

export type SubmissionInput = Omit<Submission, 'submission_id'>

export interface Effect {
  effect_id: string
  run_id: string
  description: string
  outcome: string
  evidence_ref: string
}

export interface ContextFact {
  text: string
  source_ref: string
}

export interface ContextDecision {
  text: string
  reason: string
  source_ref: string
}

export interface ContextPacket {
  schema_version: number
  task_id: string
  spec_version: number
  source_run_id: string
  objective: string
  allowed_scope: string
  acceptance_criteria: AcceptanceCriterion[]
  verified_facts: ContextFact[]
  decisions: ContextDecision[]
  open_questions: string[]
  next_actions: string[]
  artifact_refs: ArtifactRef[]
  effects: Effect[]
}

export interface CommandRequest {
  request_id: string
  action: string
  task_id?: string
  expected_version?: number
  data: Record<string, unknown>
}

export class ApiError extends Error {
  readonly code: string
  readonly messageKey: string
  readonly retryable: boolean
  readonly correlationId: string

  constructor(messageKey: string, options?: { code?: string; retryable?: boolean; correlationId?: string }) {
    super(messageKey)
    this.name = 'ApiError'
    this.code = options?.code ?? ''
    this.messageKey = messageKey
    this.retryable = options?.retryable ?? false
    this.correlationId = options?.correlationId ?? ''
  }
}

let tokenInMemory: string | null = null

export function setApiToken(token: string | null): void {
  tokenInMemory = token
}

function requireToken(): string {
  if (!tokenInMemory) throw new ApiError('error.authMissing')
  return tokenInMemory
}

async function request(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  headers.set('X-Pokegents-Token', requireToken())
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  let response: Response
  try {
    response = await fetch(`/api${path}`, { ...init, headers })
  } catch {
    throw new ApiError('error.network', { retryable: true })
  }

  if (!response.ok) {
    let errorBody: Partial<{ code: string; message_key: string; retryable: boolean; correlation_id: string }> = {}
    try {
      errorBody = await response.json()
    } catch {
      // Keep transport errors generic when the service did not return its error contract.
    }
    throw new ApiError(errorBody.message_key || 'error.requestFailed', {
      code: errorBody.code,
      retryable: errorBody.retryable,
      correlationId: errorBody.correlation_id,
    })
  }
  return response
}

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await request(path, init)
  if (response.status === 204) return undefined as T
  try {
    return await response.json() as T
  } catch {
    throw new ApiError('error.invalidResponse')
  }
}

export function fetchCapabilities(): Promise<Capabilities> {
  return json<Capabilities>('/capabilities')
}

export function fetchMcpSettings(): Promise<McpSettings> {
  return json<McpSettings>('/mcp/settings')
}

export function fetchHistoryInfo(): Promise<HistoryInfo> {
  return json<HistoryInfo>('/history/info')
}

export function listHistory(input: { project_id: string; cursor?: string | null; archived: boolean }): Promise<{ items: HistoryItem[]; next_cursor: string | null }> {
  return json<{ items: HistoryItem[]; next_cursor: string | null }>('/history/list', { method: 'POST', body: JSON.stringify(input) })
}

export function previewHistory(input: { project_id: string; thread_id: string }): Promise<HistoryPreview> {
  return json<HistoryPreview>('/history/preview', { method: 'POST', body: JSON.stringify(input) })
}

export function importHistory(input: { request_id: string; task_id: string; expected_version: number; thread_id: string; preview_sha256: string }): Promise<HistoryImportResult> {
  return json<HistoryImportResult>('/history/import', { method: 'POST', body: JSON.stringify(input) })
}

export function updateMcpSettings(input: McpSettingsUpdate): Promise<McpSettings> {
  return json<McpSettings>('/mcp/settings', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function fetchSnapshot(): Promise<Snapshot> {
  return json<Snapshot>('/snapshot')
}

export async function openProjectTerminal(input: { project_id: string; mode: 'open' | 'focus' }): Promise<{ requested: true }> {
  const result = await json<{ requested: boolean }>('/native/terminal', {
    method: 'POST',
    body: JSON.stringify(input),
  })
  if (!result.requested) throw new ApiError('error.TERMINAL_REQUEST_FAILED')
  return { requested: true }
}

export function fetchMessages(taskId: string, cursor?: string | null): Promise<Page<Message>> {
  const params = new URLSearchParams({ task_id: taskId })
  if (cursor) params.set('cursor', cursor)
  return json<Page<Message>>(`/messages?${params.toString()}`)
}

export function searchMessages(query: string, projectId?: string, cursor?: string | null): Promise<Page<unknown>> {
  const params = new URLSearchParams({ q: query })
  if (projectId) params.set('project_id', projectId)
  if (cursor) params.set('cursor', cursor)
  return json<Page<unknown>>(`/search?${params.toString()}`)
}

export function postCommand(command: CommandRequest): Promise<unknown> {
  return json<unknown>('/commands', {
    method: 'POST',
    body: JSON.stringify(command),
  })
}

export function pickDirectory(): Promise<{ path: string }> {
  return json<{ path: string }>('/native/pick-directory', {
    method: 'POST',
    body: JSON.stringify({}),
  })
}

export function openExternalURL(url: string): Promise<unknown> {
  return json<unknown>('/native/open-url', {
    method: 'POST',
    body: JSON.stringify({ url }),
  })
}

export async function consumeEvents(
  after: number,
  signal: AbortSignal,
  onConnected: () => void,
  onEvent: (sequence: number | null) => void,
): Promise<void> {
  const params = new URLSearchParams({ after: String(after) })
  const response = await request(`/events?${params.toString()}`, {
    method: 'GET',
    headers: { Accept: 'text/event-stream' },
    signal,
  })
  onConnected()
  const reader = response.body?.getReader()
  if (!reader) throw new ApiError('error.streamUnavailable', { retryable: true })

  const decoder = new TextDecoder()
  let buffer = ''
  let eventId = ''
  let hasData = false

  const consumeLine = (line: string) => {
    if (line === '') {
      if (hasData) {
        const parsedSequence = Number(eventId)
        onEvent(eventId !== '' && Number.isSafeInteger(parsedSequence) ? parsedSequence : null)
      }
      eventId = ''
      hasData = false
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    const value = colon === -1 ? '' : line.slice(colon + 1).replace(/^ /, '')
    if (field === 'id') eventId = value
    if (field === 'data') hasData = true
  }

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split(/\r?\n/)
      buffer = lines.pop() ?? ''
      for (const line of lines) consumeLine(line)
    }
    buffer += decoder.decode()
    if (buffer) consumeLine(buffer)
  } finally {
    reader.releaseLock()
  }
}
