import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { AgentState } from '../types'
import { CreatureIcon, hashString } from './CreatureIcon'
import { focusAgent, setSprite, sendPrompt, restartBackend, ProjectInfo, RoleInfo } from '../api'
import { SpritePicker } from './SpritePicker'
import { BusyBubble, DoneBubble, ReadingIndicator } from './MessageAnimations'
import { useSpriteAnimation } from './spriteAnimations'
import { PromptInput } from './PromptInput'
import { AgentMenu } from './AgentMenu'
import { StateBadge, AgentLifecycleState } from './StateBadge'
import { formatElapsed } from '../utils/elapsed'
import { renderMiniMarkdown } from '../utils/miniMarkdown'
import { useRuntimeCapabilities, capsFor } from '../utils/runtimes'
import { useAgentRename } from '../hooks/useAgentRename'
import { useAgentState } from '../hooks/useAgentState'

export function ProfilePill({ name, color }: { name: string; color?: [number, number, number] }) {
  const [r, g, b] = color || [100, 100, 100]
  return (
    <span
      className="text-xs leading-none theme-font-display theme-text-primary rounded-sm px-1 py-0 pixel-shadow shrink-0 uppercase max-w-full truncate"
      style={{ background: `rgba(${r}, ${g}, ${b}, 0.6)`, border: `1px solid rgba(${r}, ${g}, ${b}, 0.8)` }}
    >{name}</span>
  )
}

export function RolePill({ name }: { name: string }) {
  return (
    <span className="text-xs leading-none theme-font-display theme-text-primary rounded-sm px-1 py-0 pixel-shadow shrink-0 uppercase max-w-full truncate"
      style={{ background: 'var(--theme-panel-muted-bg)', border: '1px solid rgba(255,255,255,0.2)' }}
    >{name}</span>
  )
}

export const GROUP_COLORS: [number, number, number][] = [
  [168, 80, 200],  // purple
  [80, 168, 200],  // teal
  [200, 140, 60],  // amber
  [100, 180, 100], // green
  [200, 80, 120],  // rose
  [80, 120, 200],  // blue
]

export function TaskGroupPill({ name }: { name: string }) {
  const idx = Math.abs(hashString(name)) % GROUP_COLORS.length
  const [r, g, b] = GROUP_COLORS[idx]
  return (
    <span
      className="text-xs leading-none theme-font-display theme-text-primary rounded-sm px-1 py-0 pixel-shadow shrink-0 uppercase max-w-full truncate"
      style={{ background: `rgba(${r}, ${g}, ${b}, 0.6)`, border: `1px solid rgba(${r}, ${g}, ${b}, 0.8)` }}
    >{name}</span>
  )
}

function SubagentPill({ type }: { type?: string }) {
  return (
    <span
      className="text-xs leading-none theme-font-display theme-text-primary rounded-sm px-1 py-0 pixel-shadow shrink-0 uppercase max-w-full truncate"
      style={{ background: 'var(--theme-accent-blue)', border: '1px solid rgba(120, 180, 255, 0.8)' }}
    >{type || 'subagent'}</span>
  )
}

export function HealthBar({ tokens, window: ctxWindow, showNumbers = true }: { tokens: number; window: number; showNumbers?: boolean }) {
  if (!ctxWindow && !tokens) {
    return (
      <div className="flex items-center gap-1.5 mt-1">
        <span className="text-xs theme-font-display text-accent-yellow pixel-shadow shrink-0 uppercase">CTX</span>
        <div className="flex-1 h-[6px] gba-hp-bar" />
        {showNumbers && <span className="text-s theme-font-mono theme-text-faint shrink-0">—</span>}
      </div>
    )
  }

  const usage = Math.max(0, Math.min(1, tokens / (ctxWindow || 1000000)))
  const remaining = Math.max(0, ctxWindow - tokens)
  const remainingPct = (1 - usage) * 100

  let color = 'var(--theme-accent-green)'  // GBA green
  if (remainingPct < 20) color = 'var(--theme-accent-red)'  // GBA red
  else if (remainingPct < 50) color = 'var(--theme-accent-yellow)'  // GBA yellow

  const formatK = (value: number) => `${Math.round((value || 0) / 1000)}k`

  return (
    <div className="flex items-center gap-1.5 mt-1">
      <span className="text-xs theme-font-display text-accent-yellow pixel-shadow shrink-0 uppercase">CTX</span>
      <div className="flex-1 h-[6px] gba-hp-bar overflow-hidden">
        <div
          className="h-full transition-all duration-1000"
          style={{ width: `${remainingPct}%`, background: color }}
        />
      </div>
      {showNumbers && <span className="text-s theme-font-mono theme-text-muted pixel-shadow shrink-0 tabular-nums">{formatK(remaining)}/{formatK(ctxWindow)}</span>}
    </div>
  )
}

type LayoutMode = 'standard' | 'compact' | 'compact-minimal'

interface AgentCardProps {
  agent: AgentState
  onClick: () => void
  mode: LayoutMode
  spriteOverride?: string
  isReading?: boolean
  hideSprite?: boolean
  hideGroupTag?: boolean
  onCollapse?: () => void
  onDismiss?: () => void
  cardRef?: (el: HTMLDivElement | null) => void
  projects?: ProjectInfo[]
  roles?: RoleInfo[]
  existingGroups?: string[]
  /** When true, shows a brief accent-blue glow ring to indicate this card's
   *  chat panel is active on the right. */
  glowActive?: boolean
  /** Chat agent whose WebSocket hasn't connected yet. */
  isConnecting?: boolean
  /** Overrides quick-input submission; chat cards pass the WebSocket action so card input matches ChatPanel behavior. */
  quickSendPrompt?: (text: string) => void | Promise<void>
  quickInputDisabled?: boolean
  quickInputPlaceholder?: string
  quickInputBusy?: boolean
  quickInputSlashCommands?: boolean
  shortcutLabel?: string
  shortcutVisible?: boolean
}

const HIDE_DETAILS = new Set(['finished', 'session started', 'processing prompt'])

function SpriteAnimWrapper({ state, compact, children }: { state: string; compact: boolean; children: React.ReactNode }) {
  const animClass = useSpriteAnimation(state, !compact)
  // "celebrating" state uses a continuous hop, not the cycling system
  if (!compact && state === 'celebrating') {
    return <div className="relative sprite-hop-loop">{children}</div>
  }
  return <div className={`relative ${compact ? '' : animClass}`}>{children}</div>
}

export function AgentCard({ agent, onClick, mode, spriteOverride, isReading, hideSprite, hideGroupTag, onCollapse, onDismiss, cardRef, projects, roles, existingGroups, glowActive, isConnecting, quickSendPrompt, quickInputDisabled, quickInputPlaceholder, quickInputBusy, quickInputSlashCommands, shortcutLabel, shortcutVisible }: AgentCardProps) {
  const compact = mode === 'compact' || mode === 'compact-minimal'
  const showPrompt = mode === 'standard'
  const showInput = mode !== 'compact-minimal'
  const outputH = 'flex-1 min-h-0'
  const title = agent.display_name || agent.profile_name || 'Agent'
  const showDetail = agent.detail && !HIDE_DETAILS.has(agent.detail)
  const [r, g, b] = agent.color
  const agentState = useAgentState(agent)
  const backendDeadRaw = agent.interface === 'chat' && !agent.is_alive
  const [backendDeadGraceElapsed, setBackendDeadGraceElapsed] = useState(false)
  const [restartPending, setRestartPending] = useState(false)
  const { isBusy, isDone, isError, isIdle } = agentState
  const ageSeconds = agent.last_updated ? (Date.now() - new Date(agent.last_updated).getTime()) / 1000 : 0
  const isCompacting = agent.detail === 'compacting'
  const preview = agent.card_preview
  const outputText = isCompacting
    ? null
    : (preview && preview.phase !== 'empty' ? (preview.text || null) : (isBusy ? agent.last_trace : agent.last_summary))

  const rename = useAgentRename(agent.pokegent_id || agent.session_id, title)
  const [menuOpen, setMenuOpen] = useState(false)
  const [menuPos, setMenuPos] = useState({ x: 0, y: 0 })
  const [showSpritePicker, setShowSpritePicker] = useState(false)
  const [flashDismissed, setFlashDismissed] = useState(false)
  const [toast, setToast] = useState<string | null>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const allCaps = useRuntimeCapabilities()
  const caps = capsFor(allCaps, agent.interface)

  useEffect(() => {
    if (!backendDeadRaw || agent.state === 'reconfiguring' || isConnecting) {
      setBackendDeadGraceElapsed(false)
      return
    }
    const t = setTimeout(() => setBackendDeadGraceElapsed(true), 8000)
    return () => clearTimeout(t)
  }, [backendDeadRaw, agent.state, isConnecting, agent.pokegent_id, agent.session_id])

  const backendDead = backendDeadRaw && agent.state !== 'reconfiguring' && !isConnecting && backendDeadGraceElapsed

  useEffect(() => {
    if (rename.isRenaming) {
      renameInputRef.current?.focus()
      renameInputRef.current?.select()
    }
  }, [rename.isRenaming])

  // Reset flash dismissed when agent starts a new turn
  useEffect(() => {
    if (isBusy) setFlashDismissed(false)
  }, [isBusy])

  const showDoneFlash = isDone && !flashDismissed

  const acknowledgeDone = () => {
    if (!showDoneFlash) return
    setFlashDismissed(true)
    fetch(`/api/sessions/${agent.pokegent_id || agent.session_id}/acknowledge`, { method: 'POST' })
  }

  const handleContextMenu = (e: React.MouseEvent) => {
    e.preventDefault()
    setMenuPos({ x: e.clientX, y: e.clientY })
    setMenuOpen(true)
  }

  const iconSize = compact ? 20 : 32
  const textSize = 'text-m'
  const headerTags = compact ? [] : [
    agentState.backgroundTasks > 0 ? (
      <span key="background-tasks" className="text-m theme-text-warning leading-none shrink-0">
        {agentState.backgroundTasks} bg
      </span>
    ) : null,
    agent.ephemeral ? <SubagentPill key="subagent" type={agent.subagent_type} /> : null,
    agent.task_group && !hideGroupTag ? <TaskGroupPill key="task-group" name={agent.task_group} /> : null,
    agent.role ? <RolePill key="role" name={agent.role} /> : null,
    !agent.ephemeral ? <ProfilePill key="profile" name={agent.project || agent.profile_name} color={agent.project_color || agent.color} /> : null,
  ].filter(Boolean)
  // In compact mode padding stays tight — there's not enough vertical space
  // for the user's preferred padding. Standard mode honours --card-padding.
  const cardStyle: React.CSSProperties = compact
    ? { padding: '6px 8px' }
    : { padding: 'var(--card-padding, 10px)' }

  return (
    <>
      <div
        ref={cardRef ? (el) => cardRef(el) : undefined}
        onContextMenu={handleContextMenu}
        onClickCapture={acknowledgeDone}
        className={`text-left cursor-default overflow-visible flex flex-col h-full transition-all duration-300 relative group ${
          isBusy ? 'gba-card-selected' : 'gba-card'
        } ${agent.ephemeral ? 'opacity-80' : ''}`}
        style={{
          ...cardStyle,
          ...(agent.ephemeral ? { borderStyle: 'dashed' } : {}),
          ...(glowActive ? { boxShadow: '0 0 0 2px rgb(var(--theme-accent-yellow-rgb) / 0.6), 0 0 16px rgb(var(--theme-accent-yellow-rgb) / 0.2)' } : {}),
          ...(showDoneFlash ? { boxShadow: '0 0 0 2px rgb(var(--theme-accent-green-rgb) / 0.72), 0 0 18px rgb(var(--theme-accent-green-rgb) / 0.55), 0 0 34px rgb(var(--theme-accent-green-rgb) / 0.22)' } : {}),
        }}
      >
        {showDoneFlash && (
          <div className="pointer-events-none absolute inset-0 rounded-lg card-done-green-overlay z-[1]" />
        )}

        {shortcutVisible && shortcutLabel && (
          <div className="pointer-events-none absolute inset-0 rounded-lg z-30 flex items-start justify-end p-2"
            style={{ background: 'rgba(0,0,0,0.18)', boxShadow: 'inset 0 0 0 1px rgb(var(--theme-accent-yellow-rgb) / 0.38)', backdropFilter: 'blur(1px)' }}
          >
            <div
              className="theme-font-display pixel-shadow flex items-center justify-center"
              style={{
                width: 28,
                height: 28,
                borderRadius: 6,
                background: 'var(--theme-chat-tool-bg)',
                color: 'var(--theme-text-primary)',
                border: '1px solid var(--theme-panel-divider)',
                fontSize: 'var(--theme-type-xl)',
                lineHeight: '28px',
                boxShadow: 'var(--theme-shadow-panel)',
              }}
            >
              ⌘{shortcutLabel}
            </div>
          </div>
        )}

        {/* Dead chat backend overlay */}
        {backendDead && (
          <div className="absolute inset-0 rounded-lg flex items-center justify-center z-20" style={{ background: 'var(--theme-modal-scrim)' }}>
            <div className="flex flex-col items-center gap-1.5 px-3 text-center">
              <span className="text-s theme-font-display text-accent-red pixel-shadow">BACKEND OFFLINE</span>
              <button
                type="button"
                disabled={restartPending}
                onClick={async (e) => {
                  e.stopPropagation()
                  setRestartPending(true)
                  setToast('Restarting backend…')
                  try {
                    await restartBackend(agent.pokegent_id || agent.session_id)
                  } catch (err) {
                    setToast(`Restart failed: ${err instanceof Error ? err.message : String(err)}`)
                    setRestartPending(false)
                  }
                }}
                className="gba-button px-2 py-1 text-s theme-font-display theme-text-primary disabled:opacity-50"
              >
                {restartPending ? 'RESTARTING…' : 'RESTART'}
              </button>
            </div>
          </div>
        )}

        {/* Connecting overlay */}
        {isConnecting && (
          <div className="absolute inset-0 rounded-lg flex items-center justify-center z-10" style={{ background: 'var(--theme-chat-bg)' }}>
            <div className="flex items-center gap-2">
              <div className="w-3 h-3 border-2 theme-border-subtle border-t-white/90 rounded-full animate-spin" />
              <span className="text-s theme-font-display theme-text-secondary pixel-shadow">CONNECTING</span>
            </div>
          </div>
        )}

        {/* Toast overlay */}
        {toast && (
          <div className="absolute inset-0 rounded-lg flex items-center justify-center pointer-events-none z-20" style={{ background: 'var(--theme-modal-scrim)' }}>
            <span className="text-s theme-font-display text-accent-yellow pixel-shadow">{toast}</span>
          </div>
        )}

        {/* Minimize button — pokeball style in top-right corner */}
        {onCollapse && (
          <div className="absolute top-0 right-0 w-[10%] h-[15%] z-10 group/corner">
            <button
              onClick={(e) => { e.stopPropagation(); onCollapse() }}
              className="absolute top-1 right-1 w-3.5 h-3.5 rounded-full bg-accent-red hover:bg-accent-red/80 opacity-0 group-hover/corner:opacity-100 transition-opacity flex items-center justify-center text-s theme-font-display theme-text-primary leading-none"
              style={{ boxShadow: 'var(--theme-text-shadow-pixel)' }}
              title="Collapse"
            >
              −
            </button>
          </div>
        )}

        {/* Dismiss button for completed ephemeral subagents */}
        {onDismiss && agent.ephemeral && isDone && (
          <div className="absolute top-0 right-0 w-[10%] h-[15%] z-10 group/corner">
            <button
              onClick={(e) => { e.stopPropagation(); onDismiss() }}
              className="absolute top-1 right-1 w-3.5 h-3.5 rounded-full theme-bg-panel-subtle theme-bg-panel-hover opacity-0 group-hover/corner:opacity-100 transition-opacity flex items-center justify-center text-s theme-font-display theme-text-primary leading-none"
              style={{ boxShadow: 'var(--theme-text-shadow-pixel)' }}
              title="Dismiss"
            >
              ×
            </button>
          </div>
        )}

        {/* Header: icon + name + status */}
        <div className={`flex items-center ${compact ? 'gap-1.5 mb-1' : 'gap-2 mb-2'} shrink-0`}>
          {/* Click sprite → change sprite */}
          <div
            onClick={(e) => { e.stopPropagation(); setShowSpritePicker(true) }}
            className="cursor-pointer hover:brightness-125 relative overflow-visible"
            style={{ width: iconSize, height: iconSize }}
          >
            {/* Static background box */}
            {!compact && <div className="absolute inset-0 theme-bg-panel-muted rounded-lg" />}
            {/* Animated sprite + bubbles */}
            <SpriteAnimWrapper state={showDoneFlash && !isIdle && ageSeconds < 60 ? 'celebrating' : agent.state} compact={compact}>
              <div style={{ opacity: hideSprite ? 0 : 1, transition: 'opacity 0.15s' }}>
                <CreatureIcon sessionId={agent.session_id} size={iconSize} noGlow={compact} doneFlash={false} spriteOverride={spriteOverride} noBg />
              </div>
              {!compact && <BusyBubble isBusy={isBusy} />}
              {!compact && <DoneBubble isDone={isDone} />}
              <ReadingIndicator isReading={!!isReading} />
            </SpriteAnimWrapper>
          </div>
          <div className="flex-1 min-w-0 relative">
            {/* Left header column: title/state + context. Tags live in a right
                rail so they never consume title width inside the same row. */}
            {rename.isRenaming ? (
              <input
                ref={renameInputRef}
                value={rename.newName}
                onChange={(e) => rename.setNewName(e.target.value)}
                onBlur={rename.submitRename}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') rename.submitRename()
                  if (e.key === 'Escape') rename.cancelRename()
                }}
                onClick={(e) => e.stopPropagation()}
                className={`${compact ? 'text-s' : 'text-s'} theme-font-display theme-text-primary bg-transparent border-b theme-border-subtle outline-none w-full pixel-shadow`}
              />
            ) : (
              <div className="flex items-center gap-1.5 min-w-0">
                <h3
                  className={`${compact ? 'text-xs' : 'text-s'} theme-font-display theme-text-primary truncate cursor-pointer hover:text-accent-yellow pixel-shadow min-w-0`}
                  onClick={(e) => { e.stopPropagation(); rename.startRename() }}
                >
                  {title}
                </h3>
                {!compact && <StateBadge state={(agent.state || 'idle') as AgentLifecycleState} busySince={agent.busy_since} compact />}
              </div>
            )}

            <HealthBar tokens={agent.context_tokens} window={agent.context_window} showNumbers={false} />
          </div>
          {!compact && headerTags.length > 0 && (
            <div className="w-fit max-w-[24%] shrink-0 flex flex-col items-end gap-px overflow-hidden">
              {headerTags.map((tag, idx) => (
                <div key={idx} className="max-w-full flex justify-end overflow-hidden">
                  {tag}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Last prompt — shown in standard modes */}
        {showPrompt && agent.user_prompt && (
          <div
            className="rounded-md px-3 py-0.5 mb-1 shrink-0 overflow-hidden"
            style={{ background: 'var(--theme-chat-tool-bg)', boxShadow: 'var(--theme-shadow-panel)', fontSize: 'var(--agent-card-output-font-size, 10px)' }}
          >
            <div className="theme-font-mono theme-text-muted truncate leading-snug">
              <span className="theme-text-warning mr-1">&gt;</span>
              {agent.user_prompt}
            </div>
          </div>
        )}

        {/* Output box — always present, content switches based on state */}
        <ActivityBox
          agent={agent}
          isBusy={isBusy}
          isDone={isDone}
          isError={isError}
          isCompacting={isCompacting}
          outputText={outputText}
          preview={preview}
          compact={compact}
          outputH={outputH}
          textSize={textSize}
        />
        {/* Quick command input */}
        {showInput && (
          <PromptInput
            sessionId={agent.pokegent_id || agent.session_id}
            onSend={quickSendPrompt ?? ((text) => sendPrompt(agent.pokegent_id || agent.session_id, text))}
            variant="card"
            maxHeight={compact ? 72 : 120}
            maxLines={8}
            disabled={backendDead || quickInputDisabled}
            placeholder={quickInputPlaceholder ?? 'What will you do?'}
            isBusy={quickInputBusy ?? isBusy}
            enableSlashCommands={quickInputSlashCommands ?? agent.interface === 'chat'}
          />
        )}
      </div>

      {/* Right-click context menu */}
      {menuOpen && createPortal(
        <AgentMenu
          x={menuPos.x}
          y={menuPos.y}
          agent={agent}
          capabilities={caps}
          onClose={() => setMenuOpen(false)}
          onRename={() => { setMenuOpen(false); rename.startRename() }}
          onChangeSprite={() => { setMenuOpen(false); setShowSpritePicker(true) }}
          onCollapse={onCollapse}
          projects={projects}
          roles={roles}
          existingGroups={existingGroups}
          onAssignStatus={(msg) => { setToast(msg); setTimeout(() => setToast(null), 2500) }}
        />,
        document.body
      )}

      {/* Sprite picker */}
      {showSpritePicker && createPortal(
        <SpritePicker
          currentSprite={agent.sprite || 'pokeball'}
          onSelect={async (sprite) => { await setSprite(agent.session_id, sprite) }}
          onClose={() => setShowSpritePicker(false)}
        />,
        document.body
      )}
    </>
  )
}

function ActivityBox({ agent, isBusy, isDone, isError, isCompacting, outputText, preview, compact, outputH, textSize }: {
  agent: AgentState; isBusy: boolean; isDone: boolean; isError: boolean; isCompacting: boolean;
  outputText: string | null; preview?: AgentState['card_preview']; compact: boolean; outputH: string; textSize: string;
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  // If the server sent a normalized preview, trust it as the current-turn
  // source of truth. Falling back to agent.activity_feed when preview.feed is
  // omitted resurrects stale commands from the previous turn during the small
  // window after a new prompt is submitted but before new tool events arrive.
  const feed = preview ? (preview.feed ?? []) : agent.activity_feed
  const feedSignature = feed.map(item => `${item.time}|${item.type}|${item.text}`).join('\n')
  const phase = preview?.phase
  const [, setTick] = useState(0)

  // Auto-scroll to bottom when feed updates
  useEffect(() => {
    if (scrollRef.current && isBusy) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [feedSignature, isBusy])

  // Tick timer every 1s for elapsed display
  useEffect(() => {
    if (isBusy) return
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [isBusy])

  const elapsed = !isBusy && agent.last_updated ? formatElapsed(agent.last_updated) + ' ago' : ''

  return (
    <div className={`relative ${outputH}`}>
      <div
        ref={scrollRef}
        data-no-drag
        className={`rounded-md ${compact ? 'px-2 py-1.5' : 'px-3 py-2'} h-full overflow-y-auto overflow-x-hidden cursor-pointer hover:brightness-110`}
        style={{ background: 'var(--theme-chat-tool-bg)', boxShadow: 'var(--theme-shadow-panel)', fontSize: 'var(--agent-card-output-font-size, 10px)' }}
        onClick={(e) => {
          e.stopPropagation()
          // Route by interface — chat-backed agents don't have an iTerm tab to
          // focus, so open the chat panel via the same CustomEvent the migrate
          // flow uses. iTerm2 agents fall through to focusAgent as before.
          if (agent.interface === 'chat') {
            window.dispatchEvent(new CustomEvent('open-chat-panel', {
              detail: { pokegentId: agent.pokegent_id || agent.session_id },
            }))
          } else {
            focusAgent(agent.pokegent_id || agent.session_id)
          }
        }}
      >
      {isCompacting ? (
        <div className="theme-font-mono text-accent-yellow/80 animate-pulse">
          {preview?.text || 'Compacting conversation history...'}
        </div>
      ) : phase === 'error' ? (
        <div className="theme-font-mono text-accent-orange">
          ! {outputText || agent.detail || 'API error - reprompt to retry'}
        </div>
      ) : phase === 'waiting' ? (
        <div className="theme-font-mono text-accent-yellow/80">
          {outputText || agent.detail || 'Needs input'}
        </div>
      ) : isBusy && feed && feed.length > 0 ? (
        <div className="flex flex-col gap-0.5">
          {feed.map((item, i) => {
            const isLatest = i === feed.length - 1
            const textClass = item.type === 'tool'
              ? (isLatest ? 'text-accent-yellow' : 'theme-text-faint')
              : (isLatest ? 'theme-text-secondary' : 'theme-text-muted')
            return (
              <div key={i} className="theme-font-mono leading-snug activity-feed-row">
                <span className="theme-text-faint select-none activity-feed-time">{item.time}</span>
                <span className={`min-w-0 ${item.type === 'tool' ? 'truncate' : 'activity-feed-clamp'} ${textClass}`}>
                  {item.type === 'tool' && <span className="theme-text-faint mr-0.5 select-none">▸</span>}
                  {item.type === 'thinking' ? <span className="italic">{item.text}</span> : item.text}
                </span>
              </div>
            )
          })}
        </div>
      ) : outputText ? (
        <div
          className={`theme-font-mono leading-relaxed whitespace-pre-wrap ${
            isDone ? 'text-accent-green/80' : phase === 'thinking' ? 'theme-text-faint' : 'theme-text-secondary'
          } [&_strong]:font-bold [&_strong]:text-current [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:theme-bg-panel-muted`}
        >
          <span dangerouslySetInnerHTML={{ __html: renderMiniMarkdown(outputText) }} />
        </div>
      ) : isError ? (
        <div className="theme-font-mono text-accent-orange">
          ! {agent.detail || 'API error - reprompt to retry'}
        </div>
      ) : (
        <div className="theme-font-mono theme-text-faint">
          {isBusy ? 'Working...' : 'No output yet'}
        </div>
      )}
      </div>
      {/* Pinned elapsed timer — outside scroll area, shifts right when scrollbar visible */}
      {elapsed && (
        <div
          className="absolute bottom-1.5 text-s theme-font-mono theme-text-faint pointer-events-none"
          style={{ right: scrollRef.current && scrollRef.current.scrollHeight > scrollRef.current.clientHeight ? 16 : 8 }}
        >{elapsed}</div>
      )}
    </div>
  )
}
