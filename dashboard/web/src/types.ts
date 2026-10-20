export interface CardPreview {
  state: string
  phase: 'thinking' | 'tool' | 'streaming' | 'waiting' | 'complete' | 'error' | 'empty' | string
  prompt?: string
  text?: string
  feed?: { time: string; type: string; text: string }[]
  updated_at?: string
}

export interface AgentState {
  session_id: string
  pokegent_id?: string
  profile_name: string
  display_name: string
  emoji: string
  color: [number, number, number]
  role?: string
  project?: string
  role_emoji?: string
  project_color?: [number, number, number]
  task_group?: string
  model?: string
  effort?: string
  sprite?: string
  ephemeral?: boolean
  parent_session_id?: string
  subagent_type?: string
  state: string
  detail: string
  cwd: string
  last_summary: string
  last_trace: string
  user_prompt: string
  recent_actions: string[]
  activity_feed: { time: string; type: string; text: string }[]
  card_preview?: CardPreview
  context_tokens: number
  context_window: number
  last_updated: string
  busy_since: string
  pid: number
  tty: string
  is_alive: boolean
  duration_sec: number
  created_at: string
  /** Number of active background tasks (subagents, bg shells, monitors). */
  background_tasks?: number
  /** Runtime backend bound to this agent. "" / "iterm2" → terminal tab.
   *  "chat" → embedded ACP chat panel. Drives card click routing. */
  interface?: 'iterm2' | 'chat'
  /** ACP backend/provider ID (e.g. "claude", "codex") from backends.json config. */
  agent_backend?: string
}

export interface Profile {
  name: string
  title: string
  emoji: string
  color: [number, number, number]
  cwd: string
}

export interface TranscriptSummary {
  session_id: string
  started_at?: string
  last_modified: number
  custom_title: string
  first_user_msg: string
  project_dir?: string
  git_branch?: string
  cwd?: string
  snippet?: string
}

export interface PokegentSummary {
  pokegent_id: string
  display_name: string
  sprite: string
  role?: string
  project?: string
  task_group?: string
  profile_name?: string
  interface?: 'iterm2' | 'chat'
  agent_backend?: string
  role_emoji?: string
  project_color?: [number, number, number]
  created_at?: string
  last_active_at: number
  conversation_count: number
  latest_session: TranscriptSummary
  transcripts?: TranscriptSummary[]
}

export type AgentStatus = 'running' | 'idle' | 'error' | 'permission' | 'waiting' | 'started' | 'ended'

/** Stable identity for an agent — pokegent_id is the primary, session_id is the fallback. */
export function stableId(a: AgentState): string {
  return a.pokegent_id || a.session_id
}

export interface AgentMessage {
  id: string
  from: string
  from_name: string
  to: string
  to_name: string
  content: string
  timestamp: string
  delivered: boolean
}
