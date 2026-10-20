export type ContentBlock = { type: 'text'; text: string } | { type: string; [k: string]: unknown }
export type ToolStatus = 'pending' | 'in_progress' | 'completed' | 'failed'

export interface ToolCall {
  toolCallId: string
  title?: string
  kind?: string
  status?: ToolStatus
  locations?: { path: string; line?: number }[]
  rawInput?: unknown
  rawOutput?: unknown
  content?: unknown[]
  _meta?: { claudeCode?: { toolName?: string; toolResponse?: Record<string, unknown> } }
}

export type SessionUpdate =
  | { sessionUpdate: 'user_message_chunk'; content: ContentBlock; nonce?: string }
  | { sessionUpdate: 'agent_message_chunk'; content: ContentBlock }
  | { sessionUpdate: 'agent_thought_chunk'; content: ContentBlock }
  | ({ sessionUpdate: 'tool_call' } & ToolCall)
  | ({ sessionUpdate: 'tool_call_update' } & ToolCall)
  | { sessionUpdate: 'session_info_update'; title?: string }
  | { sessionUpdate: string; [k: string]: unknown }

export interface SessionUpdateEnvelope { sessionId?: string; update: SessionUpdate }

export interface PermissionOption {
  optionId: string
  name: string
  kind: 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always'
}

export type DeliveryState = 'sending' | 'confirmed' | 'failed' | 'queued'

export type Entry =
  | { kind: 'user'; id: string; text: string; ts?: number; nonce?: string; deliveryState?: DeliveryState }
  | { kind: 'assistant'; id: string; text: string; thoughts: string; ts?: number }
  | { kind: 'tool'; id: string; data: ToolCall; ts?: number }
  | { kind: 'system'; id: string; text: string; ts?: number }
  | { kind: 'permission'; id: string; requestId: number; toolCall?: ToolCall; options: PermissionOption[]; resolved?: 'allowed' | 'denied' | 'pending'; ts?: number }

export function renderRawPayload(value: unknown): string {
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

export function textOf(content: ContentBlock | undefined): string {
  if (!content) return ''
  if (content.type === 'text' && typeof (content as { text?: string }).text === 'string') {
    return (content as { text: string }).text
  }
  return ''
}
