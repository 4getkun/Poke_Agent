import { AgentState, PokegentSummary, AgentMessage } from './types'

const BASE = '/api'

export async function fetchSessions(): Promise<AgentState[]> {
  const res = await fetch(`${BASE}/sessions`)
  if (!res.ok) return []
  return res.json()
}

// ── PC box (pokegent-centric) ───────────────────────────────
export async function fetchPokegents(limit = 100): Promise<PokegentSummary[]> {
  const res = await fetch(`${BASE}/pokegents/pc-box?limit=${limit}`)
  if (!res.ok) return []
  const data = await res.json()
  return data.pokegents || []
}

export async function searchPokegents(query: string, limit = 50): Promise<{ pokegents: PokegentSummary[]; total: number }> {
  const params = new URLSearchParams({ q: query, limit: String(limit) })
  const res = await fetch(`${BASE}/pokegents/search?${params}`)
  if (!res.ok) return { pokegents: [], total: 0 }
  return res.json()
}

export async function fetchPokegent(pokegentId: string): Promise<PokegentSummary | null> {
  const res = await fetch(`${BASE}/pokegents/${pokegentId}`)
  if (!res.ok) return null
  return res.json()
}

export async function revivePokegent(pokegentId: string, compact?: 'yes' | 'no'): Promise<boolean> {
  const params = compact ? `?compact=${compact}` : ''
  const res = await fetch(`${BASE}/pokegents/${pokegentId}/revive${params}`, { method: 'POST' })
  return res.ok
}

export async function fetchSessionPreview(sessionId: string): Promise<{ user_prompt: string; last_summary: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/preview`)
  if (!res.ok) return { user_prompt: '', last_summary: '' }
  return res.json()
}

export async function focusAgent(sessionId: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/focus`, { method: 'POST' })
}

export async function renameAgent(sessionId: string, name: string): Promise<void> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/rename`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!res.ok) throw new Error(await res.text())
}

export async function assignRole(sessionId: string, role: string): Promise<{ status: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/role`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role }),
  })
  if (!res.ok) return { status: 'error' }
  return res.json()
}

export async function assignProject(sessionId: string, project: string): Promise<{ status: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/project`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project }),
  })
  if (!res.ok) return { status: 'error' }
  return res.json()
}

export async function assignTaskGroup(sessionId: string, taskGroup: string): Promise<{ status: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/task-group`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ task_group: taskGroup }),
  })
  if (!res.ok) return { status: 'error' }
  return res.json()
}

export async function setSprite(sessionId: string, sprite: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/sprite`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sprite }),
  })
}

export async function sendPrompt(sessionId: string, prompt: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/prompt`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  })
}

export async function cancelTurn(sessionId: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/cancel`, { method: 'POST' })
}

export async function restartBackend(sessionId: string): Promise<void> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/restart-backend`, { method: 'POST' })
  if (!res.ok) throw new Error(await res.text())
}

export interface RuntimeCapabilities {
  can_focus: boolean
  can_clone: boolean
  can_cancel: boolean
  has_streaming_ui: boolean
  has_permission_ui: boolean
}

export async function fetchRuntimes(): Promise<Record<string, RuntimeCapabilities>> {
  const res = await fetch(`${BASE}/runtimes`)
  if (!res.ok) return {}
  return res.json()
}

export async function checkAgentMessages(sessionId: string): Promise<void> {
  // This triggers the agent to check messages by sending a prompt
  await fetch(`${BASE}/sessions/${sessionId}/check-messages`, { method: 'POST' })
}

export async function spawnClone(sessionId: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/clone`, { method: 'POST' })
}

export async function shutdownAgent(sessionId: string): Promise<void> {
  await fetch(`${BASE}/sessions/${sessionId}/shutdown`, { method: 'POST' })
}

export async function dismissEphemeral(agentId: string): Promise<void> {
  await fetch(`${BASE}/ephemeral/${agentId}`, { method: 'DELETE' })
}

export async function releaseTaskGroup(groupName: string): Promise<{ ok: boolean; count: number }> {
  const res = await fetch(`${BASE}/task-groups/${encodeURIComponent(groupName)}/release`, { method: 'POST' })
  return res.json()
}

/** Unified launch — Phase 2 of pokegents-unified-launch.md.
 *  Single endpoint that mints a pokegent_id server-side, pre-writes the running
 *  file, and dispatches by `interface` / launch surface.
 */
export interface LaunchPokegentRequest {
  profile?: string
  role?: string
  project?: string
  name?: string
  sprite?: string
  model?: string
  effort?: string
  task_group?: string
  parent_pokegent_id?: string
  interface?: 'terminal' | 'iterm2' | 'chat'
  agent_backend?: string
}

export interface LaunchPokegentResponse {
  pokegent_id: string
  profile: string
  interface: string
}

export async function launchPokegent(req: LaunchPokegentRequest): Promise<LaunchPokegentResponse> {
  const res = await fetch(`${BASE}/pokegents/launch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

/** Phase 4: migrate an agent between runtime backends. Same pokegent_id and
 *  Claude session_id (so the JSONL transcript continues), different process. */
export async function migrateInterface(
  sessionId: string,
  to: 'iterm2' | 'chat',
): Promise<{ pokegent_id: string; interface: string; session_id: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/migrate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ to }),
  })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}


// ── Backend configs ────────────────────────────────────────
export interface BackendInfo {
  id: string
  name: string
  type: string
  default_model?: string
  models?: Record<string, { name?: string; model: string; effort?: string }>
  default?: boolean
}

export async function fetchBackends(): Promise<BackendInfo[]> {
  const res = await fetch(`${BASE}/backends`)
  if (!res.ok) return []
  return (await res.json()) ?? []
}

export async function switchBackend(sessionId: string, backend: string): Promise<{ ok: boolean; backend: string }> {
  const res = await fetch(`${BASE}/sessions/${sessionId}/switch-backend`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backend }),
  })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

export async function setRuntimeConfig(sessionId: string, model: string, effort: string): Promise<{ ok: boolean }> {
  const body: Record<string, string> = {}
  if (model) body.model = model
  if (effort) body.effort = effort
  const res = await fetch(`${BASE}/sessions/${sessionId}/runtime-config`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

export async function sendMessage(from: string, to: string, content: string): Promise<AgentMessage | null> {
  const res = await fetch(`${BASE}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ from, to, content }),
  })
  if (!res.ok) return null
  return res.json()
}

export async function fetchAgentOrder(): Promise<string[]> {
  const res = await fetch(`${BASE}/agent-order`)
  if (!res.ok) return []
  return res.json()
}

export async function saveAgentOrder(order: string[]): Promise<void> {
  await fetch(`${BASE}/agent-order`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(order),
  })
}

export interface TranscriptEntry {
  uuid: string
  type: 'user' | 'assistant' | 'tool_result' | 'system'
  timestamp: string
  content?: string
  blocks?: { type: string; text?: string; id?: string; name?: string; input?: string }[]
  tool_use_id?: string
  truncated?: boolean
  full_size?: number
  model?: string
  tokens?: { input: number; output: number; cache_read?: number; cache_create?: number }
}

export interface TranscriptPage {
  entries: TranscriptEntry[] | null
  has_more: boolean
}

export async function fetchTranscript(sessionId: string, tail = 100, after?: string): Promise<TranscriptPage> {
  const params = new URLSearchParams({ tail: String(tail) })
  if (after) params.set('after', after)
  const res = await fetch(`${BASE}/sessions/${sessionId}/transcript?${params}`)
  if (!res.ok) return { entries: null, has_more: false }
  return res.json()
}

export async function uploadImage(sessionId: string, imageBlob: Blob): Promise<{ image_num: number; path: string; ref: string } | null> {
  const form = new FormData()
  form.append('image', imageBlob, 'paste.png')
  const res = await fetch(`${BASE}/sessions/${sessionId}/image`, { method: 'POST', body: form })
  if (!res.ok) return null
  return res.json()
}

export interface ProfileInfo {
  name: string
  title: string
  emoji: string
  color: [number, number, number]
}

export async function fetchProfiles(): Promise<ProfileInfo[]> {
  const res = await fetch(`${BASE}/profiles`)
  if (!res.ok) return []
  return res.json()
}

export interface ProjectInfo {
  name: string
  title: string
  color: [number, number, number]
  model?: string
  effort?: string
}

export interface RoleInfo {
  name: string
  title: string
  emoji: string
  model?: string
  effort?: string
}

export async function fetchProjectList(): Promise<ProjectInfo[]> {
  const res = await fetch(`${BASE}/projects`)
  if (!res.ok) return []
  return (await res.json()) ?? []
}

export async function fetchRoleList(): Promise<RoleInfo[]> {
  const res = await fetch(`${BASE}/roles`)
  if (!res.ok) return []
  return (await res.json()) ?? []
}

export async function launchProfile(name: string): Promise<void> {
  // Try new launch endpoint first (supports role@project), fall back to legacy
  const res = await fetch(`${BASE}/launch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ profile: name }),
  })
  if (res.status === 404) {
    // Fallback: legacy endpoint for old servers
    await fetch(`${BASE}/profiles/${encodeURIComponent(name)}/launch`, { method: 'POST' })
  }
}

// ── Setup / onboarding ─────────────────────────────────────
export interface SetupCheck {
  state: string
  message?: string
  path?: string
}

export interface SetupPreferences {
  dashboard_open_mode?: string
  default_interface?: 'chat' | 'terminal' | 'iterm2'
  default_backend?: string
  default_project?: string
  default_role?: string
  editor_open_command?: string
  browser_open_command?: string
  onboarding_complete?: boolean
}

export interface SetupStatus {
  complete?: boolean
  data_dir?: SetupCheck
  config?: SetupCheck
  hooks?: SetupCheck
  claude_auth?: SetupCheck
  status_line?: SetupCheck
  preferences?: SetupPreferences
  onboarding_complete?: boolean
  required_actions?: string[]
  data_dir_exists?: boolean
  config_exists?: boolean
  dashboard_version?: string
  claude_cli?: boolean | string | SetupCheck
  claude_hooks?: boolean | 'current' | 'stale' | 'missing' | string
  mcp_messaging?: boolean | 'current' | 'stale' | 'missing' | SetupCheck
  node_runtime?: boolean | string | SetupCheck
  codex_backend?: boolean | string | SetupCheck
  default_project?: boolean | string | SetupCheck
  default_role?: boolean | string | SetupCheck
  launch_agent?: boolean | 'installed' | 'running' | 'missing' | 'stale' | SetupCheck
  launch_agent_installed?: boolean
  launch_agent_running?: boolean
  server_lifecycle_mode?: string
  [key: string]: unknown
}

export async function fetchSetupStatus(): Promise<SetupStatus | null> {
  const res = await fetch(`${BASE}/setup/status`)
  if (!res.ok) return null
  return res.json()
}

async function postSetupAction(path: string, body?: unknown): Promise<SetupStatus | null> {
  const res = await fetch(`${BASE}/setup/${path}`, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) throw new Error(await res.text())
  const text = await res.text()
  return text ? JSON.parse(text) : null
}

export interface ApplySetupRequest {
  repair_hooks?: boolean
  repair_mcp?: boolean
  install_launch_agent?: boolean
}

export function applySetup(req: ApplySetupRequest = {}): Promise<SetupStatus | null> {
  return postSetupAction('apply', req)
}

export function repairClaudeHooks(): Promise<SetupStatus | null> {
  return postSetupAction('repair-hooks')
}

export function repairMcpMessaging(): Promise<SetupStatus | null> {
  return postSetupAction('repair-mcp')
}

export function installLaunchAgent(): Promise<SetupStatus | null> {
  return postSetupAction('install-launch-agent')
}

export function setOpenAtLogin(enabled: boolean): Promise<SetupStatus | null> {
  return postSetupAction('open-at-login', { enabled })
}

export function setSetupPreferences(req: SetupPreferences): Promise<SetupStatus | null> {
  return postSetupAction('preferences', req)
}

export function completeOnboarding(): Promise<SetupStatus | null> {
  return postSetupAction('onboarding/complete')
}

export function installDefaultRoles(): Promise<SetupStatus | null> {
  return postSetupAction('defaults/roles')
}

export function createDefaultProject(req: { name?: string; title?: string; cwd?: string } = {}): Promise<SetupStatus | null> {
  return postSetupAction('defaults/project', req)
}

export function openSetupConfig(target: 'pokegents' | 'claude' | 'codex' | 'backends' | 'config' = 'pokegents', command?: string): Promise<unknown> {
  return postSetupAction('open-config', { target, command })
}

export function openSetupAuth(backend?: string): Promise<unknown> {
  return postSetupAction('open-auth', { backend })
}

export async function setDefaultBackend(id: string): Promise<{ ok: boolean; default_id: string }> {
  const res = await fetch(`${BASE}/backends/${encodeURIComponent(id)}/default`, { method: 'POST' })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}
