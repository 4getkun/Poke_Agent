import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Entry, ToolCall, renderRawPayload } from '../types/chat'
import { Markdown } from './Markdown'
import { formatElapsed } from '../utils/elapsed'
import { parseTool, normalizeToolHeader } from '../utils/toolAdapters'
import { HighlightText } from './ChatSearch'
import { TimestampRow } from './PanelViews'
import { FilePathLink } from './FilePathMenu'

// ── Diff display components ───────────────────────────────

export function DiffSummaryLabel({ oldStr, newStr, override }: { oldStr: string; newStr: string; override?: string }) {
  const oldLines = oldStr ? oldStr.split('\n').length : 0
  const newLines = newStr ? newStr.split('\n').length : 0
  const prefix = override ? override.replace(/\(\d+ lines\)/, '').trim() + ' · ' : ''
  return (
    <span>
      {prefix}
      {oldLines > 0 && <span className="theme-text-danger">−{oldLines}</span>}
      {oldLines > 0 && newLines > 0 && ' '}
      {newLines > 0 && <span className="theme-text-success">+{newLines}</span>}
    </span>
  )
}

export function FullDiffView({ oldStr, newStr }: { oldStr: string; newStr: string }) {
  const oldLines = oldStr ? oldStr.split('\n') : []
  const newLines = newStr ? newStr.split('\n') : []
  return (
    <pre data-selectable-text className="text-m theme-font-mono theme-bg-panel-muted rounded px-2 py-1 max-h-80 overflow-auto whitespace-pre-wrap break-all">
      {oldLines.map((l, i) => (
        <div key={`o${i}`} className="theme-text-danger theme-bg-diff-remove">−{l}</div>
      ))}
      {oldLines.length > 0 && newLines.length > 0 && <div className="h-px theme-bg-panel-subtle my-1" />}
      {newLines.map((l, i) => (
        <div key={`n${i}`} className="theme-text-success theme-bg-diff-add">+{l}</div>
      ))}
    </pre>
  )
}

function patchLineClass(line: string): string {
  if (line.startsWith('+') && !line.startsWith('+++')) return 'theme-text-success theme-bg-diff-add'
  if (line.startsWith('-') && !line.startsWith('---')) return 'theme-text-danger theme-bg-diff-remove'
  if (line.startsWith('@@')) return 'theme-text-accent'
  if (line.startsWith('***')) return 'theme-text-faint'
  return 'theme-text-muted'
}

function PatchDiffView({ patch, preview = false }: { patch: string; preview?: boolean }) {
  const allLines = patch.split('\n').filter(line => {
    if (!line.trim()) return !preview
    if (line === '*** Begin Patch' || line === '*** End Patch') return false
    return true
  })
  const previewLines = allLines.filter(line => (
    (line.startsWith('+') && !line.startsWith('+++')) ||
    (line.startsWith('-') && !line.startsWith('---'))
  ))
  const visible = preview ? previewLines.slice(0, 8) : allLines
  const hasMore = preview ? previewLines.length > visible.length : false

  return (
    <pre data-selectable-text className={`text-m theme-font-mono theme-bg-panel-muted rounded px-2 py-1 overflow-auto whitespace-pre-wrap break-all ${preview ? 'max-h-24 overflow-hidden' : 'max-h-80'}`}>
      {visible.map((line, i) => (
        <div key={i} className={patchLineClass(line)}>{line}</div>
      ))}
      {hasMore && <div className="theme-text-faint mt-0.5">… click to expand</div>}
    </pre>
  )
}

// isLocalCommandArtifact matches the pseudo-XML wrappers Claude Code writes
// into the transcript when a slash-command runs (`/model`, `/clear`, etc.).
// These show up as user-type JSONL entries but they're plumbing, not
// conversation — hide them from the chat panel backfill.
export function isLocalCommandArtifact(text: string): boolean {
  const t = text.trimStart()
  return (
    t.startsWith('<local-command-') ||
    t.startsWith('<command-name>') ||
    t.startsWith('<command-message>') ||
    t.startsWith('<command-args>') ||
    t.startsWith('<command-stdout>')
  )
}

export function extractFilePath(text: string): string | null {
  const m = text.match(/(\/(?:Users|private|tmp|home|var|opt)\/\S+\.\w+)/)
  return m ? m[1] : null
}

export function AnimatedDots() {
  const [count, setCount] = useState(0)
  useEffect(() => {
    const iv = setInterval(() => setCount(c => (c + 1) % 4), 500)
    return () => clearInterval(iv)
  }, [])
  return <span className="inline-block w-[1.5em] text-left">{'.'.repeat(count)}</span>
}

// TypewriterMarkdown reveals text character-by-character when new chunks
// arrive, giving the streaming a smooth animated feel instead of jarring
// block updates. Even when the provider flushes a large final chunk at turn
// completion, keep advancing toward the target instead of snapping to it.
export function TypewriterMarkdown({ text }: { text: string }) {
  const [displayed, setDisplayed] = useState(text)
  const targetRef = useRef(text)
  const rafRef = useRef<number | null>(null)
  const lastTimeRef = useRef(0)

  // Chars per frame tick. At 60fps each tick is ~16ms.
  // 9 chars/tick ≈ 540 chars/sec — quick enough to catch up after a
  // transcript flush, but still visibly animated instead of an instant dump.
  const CHARS_PER_TICK = 9

  useEffect(() => {
    targetRef.current = text
    if (text.length <= displayed.length) {
      setDisplayed(text)
      return
    }
    if (rafRef.current != null) return
    const step = (ts: number) => {
      if (ts - lastTimeRef.current < 12) {
        rafRef.current = requestAnimationFrame(step)
        return
      }
      lastTimeRef.current = ts
      setDisplayed(prev => {
        const target = targetRef.current
        if (prev.length >= target.length) {
          rafRef.current = null
          return target
        }
        const next = target.slice(0, prev.length + CHARS_PER_TICK)
        rafRef.current = requestAnimationFrame(step)
        return next
      })
    }
    rafRef.current = requestAnimationFrame(step)
    return () => {
      if (rafRef.current != null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [text])

  useEffect(() => () => {
    if (rafRef.current != null) cancelAnimationFrame(rafRef.current)
  }, [])

  return <Markdown>{displayed}</Markdown>
}

// lastEntryIsCurrentAssistantMessage returns true when the tail of the
// transcript is an assistant message that's actively streaming USER-VISIBLE
// text. We use this to suppress the "Thinking…" indicator once the agent
// has started emitting text — the streaming text itself is the activity
// indicator. Thinking-only entries (thoughts but no text) DON'T qualify:
// we keep "Thinking…" visible while the agent ruminates because we
// collapse the thoughts content by default (user can expand if curious).
export function lastEntryIsCurrentAssistantMessage(entries: Entry[]): boolean {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]
    if (e.kind === 'user' || e.kind === 'permission' || e.kind === 'system') return false
    if (e.kind === 'assistant') return !!e.text
    // tool call: keep looking — Claude often interleaves a tool call before
    // the final text response, but we still want "Thinking…" to show while
    // the tool runs.
  }
  return false
}

// ThoughtsDisclosure shows a "Show thinking" disclosure that, when clicked,
// reveals the agent's chain-of-thought. Collapsed by default — the live
// "Thinking…" indicator at the transcript tail is sufficient feedback
// during streaming; the full thoughts are useful only when post-mortem'ing
// a turn (debug or curiosity).
export function ThoughtsDisclosure({ thoughts }: { thoughts: string }) {
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  // When the user opens the disclosure at the bottom of the transcript,
  // bring the expanded body into view (scrolls only as much as needed).
  useEffect(() => {
    if (open) bodyRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open])
  return (
    <div className="mb-1.5">
      <button
        onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
        className="text-s theme-font-display uppercase pixel-shadow theme-text-faint theme-hover-text-secondary flex items-center gap-1 transition-colors"
      >
        <span className="text-s">{open ? '▼' : '▶'}</span>
        {open ? 'HIDE THINKING' : 'SHOW THINKING'}
      </button>
      {open && (
        <div ref={bodyRef} className="text-l theme-font-mono theme-text-muted italic whitespace-pre-wrap break-words mt-1 pl-3 border-l theme-border-subtle leading-snug">
          {thoughts}
        </div>
      )}
    </div>
  )
}

// inflightThoughts returns the streaming thoughts of the assistant entry
// currently being generated, so the ThinkingIndicator can disclose them
// inline. Returns "" if there's no in-progress assistant entry.
export function inflightThoughts(entries: Entry[]): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]
    if (e.kind === 'user' || e.kind === 'permission' || e.kind === 'system') return ''
    if (e.kind === 'assistant') {
      // Only "in flight" if there's no visible text yet — once text streams,
      // the ThinkingIndicator vanishes and the assistant entry's own
      // disclosure takes over.
      return e.text ? '' : (e.thoughts || '')
    }
  }
  return ''
}

// ThinkingIndicator renders a Claude-Code-style "Ruminating…" line with
// pulsing dots and a live elapsed timer. Visible while the agent is busy
// but hasn't yet streamed any assistant text for the current turn. When
// the agent has emitted thinking content (chain-of-thought), the
// indicator gets a `▶` toggle that expands the streaming thoughts inline
// so the indicator + thoughts disclosure are one combined row instead of
// two separate ones.
export function ThinkingIndicator({ busySince, thoughts, label = 'Thinking' }: { busySince?: string | null; thoughts?: string; label?: string }) {
  const [, setTick] = useState(0)
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [])
  useEffect(() => {
    if (open) bodyRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open])
  const elapsed = formatElapsed(busySince)
  const hasThoughts = !!thoughts && thoughts.trim() !== ''
  return (
    <div className="text-l theme-font-mono">
      <div className="flex items-baseline gap-2 theme-text-muted italic">
        <span className="thinking-dots">{label}</span>
        {elapsed && <span className="theme-text-faint not-italic text-m">({elapsed})</span>}
        {hasThoughts && (
          <button
            onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
            className="theme-text-faint theme-hover-text-secondary not-italic text-s theme-font-display uppercase pixel-shadow flex items-center gap-1 transition-colors"
          >
            <span className="text-s">{open ? '▼' : '▶'}</span>
            {open ? 'HIDE' : 'SHOW'}
          </button>
        )}
      </div>
      {open && hasThoughts && (
        <div ref={bodyRef} className="text-l theme-font-mono theme-text-muted italic whitespace-pre-wrap break-words mt-1 pl-3 border-l theme-border-subtle leading-snug">
          {thoughts}
        </div>
      )}
    </div>
  )
}

// Compact task notification — shows summary with click-to-expand for full XML.
export function TaskNotificationRow({ text, summary }: { text: string; summary: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div
      className="rounded-md text-m theme-font-mono leading-snug"
      style={{ background: 'var(--theme-chat-message-system-bg)' }}
    >
      <button
        onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
        className="w-full flex items-center gap-1.5 theme-text-muted px-2.5 py-1.5 theme-bg-panel-hover transition-colors rounded-md text-left"
      >
        <span className="w-1.5 h-1.5 rounded-full shrink-0 bg-accent-green" />
        <span className="theme-text-secondary truncate flex-1">{summary}</span>
        <span className="text-m theme-text-faint shrink-0">{open ? '▼' : '▶'}</span>
      </button>
      {open && (
        <pre data-selectable-text className="text-m theme-font-mono theme-text-faint theme-bg-panel-muted rounded mx-2.5 mb-2 px-2 py-1 max-h-40 overflow-auto whitespace-pre-wrap break-all">
          {text}
        </pre>
      )}
    </div>
  )
}

// Left/right arrows to jump between user prompts in the transcript.
export function PromptNav({ entries, scrollRef }: { entries: Entry[]; scrollRef: React.RefObject<HTMLDivElement | null> }) {
  const userIndices = entries.reduce<number[]>((acc, e, i) => {
    if (e.kind === 'user') acc.push(i)
    return acc
  }, [])
  const [cursor, setCursor] = useState(-1) // -1 = no selection

  if (userIndices.length === 0) return null

  function jumpTo(idx: number) {
    setCursor(idx)
    const el = scrollRef.current
    if (!el) return
    // Find the nth user entry DOM node inside the scroll container.
    // Entry rows are direct children of the scroll div's inner content.
    const userDivs = el.querySelectorAll('[data-user-entry]')
    const target = userDivs[idx]
    if (target) target.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }

  const canPrev = userIndices.length > 0 && (cursor === -1 ? true : cursor > 0)
  const canNext = cursor >= 0 && cursor < userIndices.length - 1

  return (
    <span className="flex items-center gap-1">
      <span className="text-m theme-font-mono theme-text-faint mr-1">
        {cursor >= 0 ? `${cursor + 1}/${userIndices.length}` : `${userIndices.length} prompts`}
      </span>
      <button
        onClick={() => jumpTo(cursor <= 0 ? userIndices.length - 1 : cursor - 1)}
        disabled={!canPrev}
        className="text-m theme-text-faint theme-hover-text-secondary disabled:opacity-20 px-1"
        title="Previous prompt"
      >◀</button>
      <button
        onClick={() => jumpTo(cursor < 0 ? 0 : cursor + 1)}
        disabled={!canNext}
        className="text-m theme-text-faint theme-hover-text-secondary disabled:opacity-20 px-1"
        title="Next prompt"
      >▶</button>
    </span>
  )
}

export function CollapsibleUserPrompt({ text, borderClass, searchQuery }: { text: string; borderClass: string; searchQuery: string }) {
  const [expanded, setExpanded] = useState(false)
  const lines = text.split('\n')
  const preview = lines.slice(0, 3).join('\n')
  return (
    <div
      className={`rounded-md px-2.5 py-2 text-l theme-font-mono theme-text-primary break-words leading-snug cursor-pointer ${borderClass}`}
      style={{ background: 'var(--theme-chat-message-user-bg)' }}
      onClick={() => setExpanded(e => !e)}
    >
      <div className="flex items-start justify-between gap-2">
        <div data-selectable-text className={`flex-1 ${expanded ? 'whitespace-pre-wrap' : ''}`}>
          <span className="theme-text-warning mr-1">&gt;</span>
          {expanded
            ? <HighlightText text={text} query={searchQuery} />
            : <><HighlightText text={preview} query={searchQuery} /><span className="theme-text-faint">…</span></>
          }
        </div>
        <span className="text-m theme-text-faint shrink-0 pt-0.5">{lines.length} lines</span>
      </div>
    </div>
  )
}

// Expandable tool-call row with a tinted background so it reads as a
// distinct "event" block between assistant prose. Click the header to
// toggle the detail pane (locations, raw input/output when available).
export function ToolCallRow({ entry, backgrounded }: { entry: Extract<Entry, { kind: 'tool' }>; backgrounded?: boolean }) {
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  const startRef = useRef(Date.now())
  const [, setTick] = useState(0)
  useEffect(() => {
    if (open) bodyRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open])
  const t = entry.data
  const status = t.status || 'pending'
  const isRunning = status === 'pending' || status === 'in_progress'

  useEffect(() => {
    if (!isRunning) return
    const iv = setInterval(() => setTick(n => n + 1), 1000)
    return () => clearInterval(iv)
  }, [isRunning])

  const statusDot =
    status === 'completed' ? 'bg-accent-green' :
    status === 'failed' ? 'bg-accent-red' :
    isRunning ? 'bg-accent-yellow animate-pulse' : 'theme-bg-panel-subtle'

  const elapsedStr = isRunning ? (() => {
    const sec = Math.floor((Date.now() - startRef.current) / 1000)
    return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m ${sec % 60}s`
  })() : null

  // Parse via adapter system
  const parsed = useMemo(() => parseTool(t as any), [t])
  const { header: { label: rawHeaderLabel, detail: rawHeaderDetail, filePath, description: headerDesc }, diff: parsedDiff, patch: parsedPatch, bashCommand: bashCmd, bashDescription: bashDesc } = parsed
  const { label: headerLabel, detail: headerDetail } = normalizeToolHeader(rawHeaderLabel, rawHeaderDetail)

  const isBash = headerLabel === 'Bash'
  const isEdit = headerLabel === 'Update' || headerLabel === 'Write' || headerLabel === 'Notebook'
  const hasDiff = !!parsedDiff
  const hasPatch = !!parsedPatch
  // Keep read/search/etc. in the same full-size card style as mutation tools.
  const isMiniReadOnly = false
  const diffOld = parsedDiff?.oldStr
  const diffNew = parsedDiff?.newStr

  // Extract summary text from content/rawOutput
  const summaryText = (() => {
    let raw = ''
    if (t.content && Array.isArray(t.content)) {
      for (const item of t.content as any[]) {
        if (item?.type === 'content' && item?.content?.type === 'text') { raw = renderRawPayload(item.content.text); break }
        if (item?.type === 'terminal' && item?.terminalOutput) { raw = renderRawPayload(item.terminalOutput); break }
      }
    }
    if (!raw && typeof t.rawOutput === 'string') raw = t.rawOutput
    raw = raw.replace(/^```\w*\n?/gm, '').replace(/\n?```$/gm, '').trim()
    return raw
  })()
  const truncSummary = summaryText.length > 200 ? summaryText.slice(0, 200) + '…' : summaryText

  const editSummary = (() => {
    if (parsed.summaryOverride) return parsed.summaryOverride
    if (!isEdit) return null
    if (hasDiff && diffOld != null && diffNew != null) {
      const oldLines = diffOld.split('\n').length
      const newLines = diffNew.split('\n').length
      const added = Math.max(0, newLines - oldLines)
      const removed = Math.max(0, oldLines - newLines)
      const parts: string[] = []
      if (added > 0) parts.push(`Added ${added} lines`)
      if (removed > 0) parts.push(`Removed ${removed} lines`)
      if (parts.length === 0) parts.push(`Changed ${Math.min(oldLines, newLines)} lines`)
      return parts.join(', ')
    }
    if (summaryText) return summaryText.split('\n')[0].slice(0, 80)
    return status === 'completed' ? 'Updated' : null
  })()

  if (isMiniReadOnly) {
    const summary = parsed.summaryOverride || headerDesc || bashDesc
    return (
      <div
        className={`rounded-md px-2.5 py-1 text-m theme-font-mono leading-snug min-w-0 overflow-hidden ${isRunning ? 'theme-bg-chat-system' : 'theme-bg-chat-assistant'}`}
      >
        <div className="flex items-baseline gap-1.5 min-w-0">
          <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${statusDot}`} />
          <span className="theme-text-secondary font-semibold shrink-0">{headerLabel}</span>
          {headerDetail && (() => {
            const linkPath = filePath || extractFilePath(headerDetail)
            if (linkPath) {
              return (
                <FilePathLink
                  path={linkPath}
                  className="theme-text-faint truncate hover:text-accent-blue hover:underline"
                  hitAreaClassName="inline-block min-w-0 max-w-full text-left align-baseline"
                  title={linkPath}
                >{headerDetail}</FilePathLink>
              )
            }
            return <span className="theme-text-faint min-w-0 truncate">{headerDetail}</span>
          })()}
          {summary && <span className="theme-text-faint min-w-0 truncate">· {summary}</span>}
          {elapsedStr && <span className="text-accent-yellow/50 shrink-0">· {elapsedStr}</span>}
        </div>
      </div>
    )
  }

  return (
    <div
      className={`rounded-md text-l theme-font-mono leading-snug cursor-pointer transition-colors min-w-0 overflow-hidden ${isRunning ? 'theme-bg-chat-system' : 'theme-bg-chat-assistant theme-bg-panel-hover'}`}
      onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
    >
      {/* Header */}
      <div className="w-full text-left px-2.5 py-1.5">
        <div className="grid grid-cols-[auto_minmax(min-content,max-content)_minmax(0,1fr)_auto] items-start gap-x-1.5 gap-y-0.5 min-w-0">
          <span className={`w-1.5 h-1.5 rounded-full shrink-0 mt-[5px] ${statusDot}`} />
          <span className="theme-text-primary font-semibold min-w-0 whitespace-nowrap">{headerLabel}</span>
          {headerDetail && (() => {
            const linkPath = filePath || extractFilePath(headerDetail)
            if (linkPath) {
              return (
                <FilePathLink
                  path={linkPath}
                  className="theme-text-muted break-words whitespace-normal hover:text-accent-blue hover:underline"
                  hitAreaClassName="inline min-w-0 max-w-full text-left align-baseline"
                  title={linkPath}
                >({headerDetail})</FilePathLink>
              )
            }
            return <span className="theme-text-muted min-w-0 break-words whitespace-normal">({headerDetail})</span>
          })()}
          <span className="text-m theme-text-faint shrink-0 justify-self-end">{open ? '▼' : '▶'}</span>
        </div>
        {/* Inline summary — always visible, with colored +/- counts */}
        {(editSummary || headerDesc || bashDesc) && (
          <div className="mt-0.5 ml-4 text-m theme-text-faint">
            {hasDiff ? <DiffSummaryLabel oldStr={diffOld || ''} newStr={diffNew || ''} override={parsed.summaryOverride} /> : (editSummary || headerDesc || bashDesc)}
          </div>
        )}
      </div>

      {/* Inline diff preview for edits — shown without expanding (skip for notebooks — summary line is enough) */}
      {isEdit && (hasDiff || hasPatch) && !open && headerLabel !== 'Notebook' && (
        <div className="px-2.5 pb-1.5 ml-2">
          {hasPatch ? (
            <PatchDiffView patch={parsedPatch || ''} preview />
          ) : (
            <pre data-selectable-text className="text-m theme-font-mono theme-bg-panel-muted rounded px-2 py-1 max-h-24 overflow-hidden whitespace-pre-wrap break-all">
              {(() => {
                const lines: { text: string; type: '+' | '-' | ' ' }[] = []
                const oldLines = (diffOld || '').split('\n')
                const newLines = (diffNew || '').split('\n')
                for (const l of oldLines.slice(0, 4)) lines.push({ text: l, type: '-' })
                for (const l of newLines.slice(0, 4)) lines.push({ text: l, type: '+' })
                return lines.slice(0, 8).map((l, i) => (
                  <div key={i} className={l.type === '+' ? 'theme-text-success theme-bg-diff-add' : l.type === '-' ? 'theme-text-danger theme-bg-diff-remove' : 'theme-text-faint'}>
                    {l.type}{l.text}
                  </div>
                ))
              })()}
              {((diffOld || '').split('\n').length + (diffNew || '').split('\n').length > 8) && (
                <div className="theme-text-faint mt-0.5">… click to expand</div>
              )}
            </pre>
          )}
        </div>
      )}

      {/* Inline bash output preview — truncated, shown without expanding */}
      {isBash && truncSummary && !open && (
        <div className="px-2.5 pb-1.5 ml-2">
          <pre data-selectable-text className="text-m theme-font-mono theme-text-muted theme-bg-panel-muted rounded px-2 py-1 max-h-16 overflow-hidden whitespace-pre-wrap break-all">
            {truncSummary}
          </pre>
        </div>
      )}

      {/* Running indicator */}
      {isRunning && elapsedStr && (
        <div className={`px-2.5 pb-1.5 text-l theme-font-mono ${backgrounded ? 'theme-text-accent' : 'text-accent-yellow/60'}`}>
          {backgrounded ? `↳ running in background · ${elapsedStr}` : <><AnimatedDots /> tool call in progress — {elapsedStr}</>}
        </div>
      )}
      {open && (
        <div ref={bodyRef} className="px-2.5 pb-2 space-y-1 min-w-0 overflow-hidden">
          {t.locations && t.locations.length > 0 && (
            <div className="text-m theme-font-mono theme-text-faint truncate">
              {t.locations.map(l => l.path + (l.line ? `:${l.line}` : '')).join(', ')}
            </div>
          )}
          {/* Full diff view for edits/notebooks — replaces raw dump */}
          {hasDiff ? (
            <FullDiffView oldStr={diffOld || ''} newStr={diffNew || ''} />
          ) : hasPatch ? (
            <PatchDiffView patch={parsedPatch || ''} />
          ) : (<>
            {t.rawInput != null && (
              <div>
                <div className="text-m theme-text-faint mb-0.5">Input</div>
                <pre data-selectable-text className="text-m theme-font-mono theme-text-muted theme-bg-panel-muted rounded px-2 py-1 max-h-40 overflow-auto whitespace-pre-wrap break-all">
                  {renderRawPayload(t.rawInput)}
                </pre>
              </div>
            )}
          </>)}
          {/* Consolidated output: use content array if available, fall back to rawOutput.
              Never show both — they contain the same data in different formats. */}
          {t.content && t.content.length > 0 ? (
            <pre data-selectable-text className="text-m theme-font-mono theme-text-muted theme-bg-panel-muted rounded px-2 py-1 max-h-60 overflow-auto whitespace-pre-wrap break-all">
              {(t.content as any[]).map((item: any, i: number) => {
                if (item?.type === 'content' && item?.content?.type === 'text') return <span key={i}>{item.content.text}</span>
                if (item?.type === 'terminal' && item?.terminalOutput) return <span key={i}>{item.terminalOutput}</span>
                if (item?.type === 'terminal' && item?.command) return <span key={i} className="text-accent-yellow/70">$ {item.command}{'\n'}</span>
                if (item?.type === 'diff') {
                  const diffText: string = item.diff || item.newContent || JSON.stringify(item, null, 2)
                  return <span key={i}>{diffText.split('\n').map((line: string, li: number) => {
                    const color = line.startsWith('+') ? 'theme-text-success' : line.startsWith('-') ? 'theme-text-danger' : line.startsWith('@@') ? 'theme-text-accent' : 'theme-text-muted'
                    return <div key={li} className={color}>{line}</div>
                  })}</span>
                }
                return <span key={i}>{renderRawPayload(item)}</span>
              })}
            </pre>
          ) : !hasDiff && !hasPatch && t.rawOutput != null ? (
            <div>
              <div className="text-m theme-text-faint mb-0.5">Output</div>
              <pre data-selectable-text className="text-m theme-font-mono theme-text-muted theme-bg-panel-muted rounded px-2 py-1 max-h-60 overflow-auto whitespace-pre-wrap break-all">
                {renderRawPayload(t.rawOutput)}
              </pre>
            </div>
          ) : null}
          {!t.locations?.length && t.rawInput == null && t.rawOutput == null && (!t.content || t.content.length === 0) && !hasDiff && !hasPatch && (
            <div className="text-m theme-text-faint italic">No details available</div>
          )}
        </div>
      )}
    </div>
  )
}

function getToolDisplay(entry: Extract<Entry, { kind: 'tool' }>) {
  const t = entry.data
  const parsed = parseTool(t as any)
  const { header: { label: rawHeaderLabel, detail: rawHeaderDetail }, diff: parsedDiff, patch: parsedPatch } = parsed
  const { label, detail } = normalizeToolHeader(rawHeaderLabel, rawHeaderDetail)
  const isEdit = label === 'Update' || label === 'Write' || label === 'Notebook'
  const isConsolidatable = parsed.effect === 'read' && !isEdit && !parsedDiff && !parsedPatch
  return { parsed, label, detail, isConsolidatable }
}

export function isConsolidatableToolEntry(entry: Entry): entry is Extract<Entry, { kind: 'tool' }> {
  return entry.kind === 'tool' && getToolDisplay(entry).isConsolidatable
}

function plural(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`
}

export function ToolCallGroupRow({
  entries,
  backgroundedToolIds,
  showTimestamps,
}: {
  entries: Extract<Entry, { kind: 'tool' }>[]
  backgroundedToolIds?: Set<string>
  showTimestamps?: boolean | 'debug'
}) {
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  const displays = useMemo(() => entries.map(getToolDisplay), [entries])
  useEffect(() => {
    if (open) bodyRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open, entries.length])

  const uniqueEntries = useMemo(() => {
    const out: Extract<Entry, { kind: 'tool' }>[] = []
    const seen = new Set<string>()
    for (const entry of entries) {
      const d = getToolDisplay(entry)
      const filePath = d.parsed.header.filePath || d.parsed.fileOps?.[0]?.path || ''
      const key = `${d.label}:${filePath || d.detail || entry.data.toolCallId}`
      if (seen.has(key)) continue
      seen.add(key)
      out.push(entry)
    }
    return out
  }, [entries])
  const uniqueDisplays = useMemo(() => uniqueEntries.map(getToolDisplay), [uniqueEntries])

  const statuses = entries.map(e => e.data.status || 'pending')
  const isRunning = statuses.some(s => s === 'pending' || s === 'in_progress')
  const hasFailed = statuses.some(s => s === 'failed')
  const statusDot =
    hasFailed ? 'bg-accent-red' :
    isRunning ? 'bg-accent-yellow animate-pulse' :
    'bg-accent-green'

  const counts = uniqueDisplays.reduce<Record<string, number>>((acc, d) => {
    const label = d.label === 'List' ? 'Read' : d.label || 'Read'
    acc[label] = (acc[label] || 0) + 1
    return acc
  }, {})
  const labelSummary = (() => {
    const parts: string[] = []
    if (counts.Read) {
      const filePaths = new Set<string>()
      for (const d of uniqueDisplays) {
        for (const op of d.parsed.fileOps || []) {
          if (op.path) filePaths.add(op.path)
        }
        if (d.parsed.header.filePath) filePaths.add(d.parsed.header.filePath)
      }
      parts.push(`Read ${plural(filePaths.size || counts.Read, filePaths.size ? 'file' : 'item')}`)
    }
    if (counts.Search) parts.push(`Search ${plural(counts.Search, 'query', 'queries')}`)
    if (counts.Fetch) parts.push(`Fetch ${plural(counts.Fetch, 'page')}`)
    const known = (counts.Read || 0) + (counts.Search || 0) + (counts.Fetch || 0)
    if (uniqueEntries.length > known) parts.push(plural(uniqueEntries.length - known, 'tool call'))
    return parts.join(' · ') || plural(uniqueEntries.length, 'tool call')
  })()

  const preview = uniqueDisplays
    .map(d => d.detail || d.parsed.header.description || d.parsed.summaryOverride || '')
    .filter(Boolean)
    .slice(0, 3)
    .join(' · ')

  const content = (
    <div
      className={`rounded-md text-l theme-font-mono leading-snug cursor-pointer transition-colors min-w-0 overflow-hidden ${isRunning ? 'theme-bg-chat-system' : 'theme-bg-chat-assistant theme-bg-panel-hover'}`}
      onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
    >
      <div className="w-full text-left px-2.5 py-1.5">
        <div className="grid grid-cols-[auto_minmax(min-content,max-content)_minmax(0,1fr)_auto] items-start gap-x-1.5 gap-y-0.5 min-w-0">
          <span className={`w-1.5 h-1.5 rounded-full shrink-0 mt-[5px] ${statusDot}`} />
          <span className="theme-text-primary font-semibold min-w-0 whitespace-nowrap">{labelSummary}</span>
          {preview && <span className="theme-text-muted min-w-0 truncate">({preview})</span>}
          <span className="text-m theme-text-faint shrink-0 justify-self-end">{open ? '▼' : '▶'}</span>
        </div>
      </div>
      {open && (
        <div ref={bodyRef} className="px-2.5 pb-2 space-y-1 min-w-0 overflow-hidden">
          {uniqueEntries.map(e => (
            <ToolCallRow key={e.id} entry={e} backgrounded={backgroundedToolIds?.has(e.id)} />
          ))}
        </div>
      )}
    </div>
  )

  return (
    <TimestampRow ts={entries[0]?.ts} showTimestamps={showTimestamps}>
      {content}
    </TimestampRow>
  )
}

export function ToolCallSummaryBlock({
  entries,
  backgroundedToolIds,
  showTimestamps,
}: {
  entries: Extract<Entry, { kind: 'tool' }>[]
  backgroundedToolIds?: Set<string>
  showTimestamps?: boolean | 'debug'
}) {
  const [open, setOpen] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (open) bodyRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open, entries.length])

  const statuses = entries.map(e => e.data.status || 'completed')
  const isRunning = statuses.some(s => s === 'pending' || s === 'in_progress')
  const hasFailed = statuses.some(s => s === 'failed')
  const statusDot =
    hasFailed ? 'bg-accent-red' :
    isRunning ? 'bg-accent-yellow animate-pulse' :
    'bg-accent-green'

  const labelSummary = plural(entries.length, 'tool call')
  const preview = useMemo(() => {
    const counts = entries.reduce<Record<string, number>>((acc, entry) => {
      const { label } = getToolDisplay(entry)
      const normalized = label || 'Tool'
      acc[normalized] = (acc[normalized] || 0) + 1
      return acc
    }, {})
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .slice(0, 4)
      .map(([label, count]) => `${label} ${count}`)
      .join(' · ')
  }, [entries])

  const content = (
    <div
      className="rounded-md text-l theme-font-mono leading-snug cursor-pointer transition-colors min-w-0 overflow-hidden theme-bg-chat-assistant theme-bg-panel-hover"
      onClick={(event) => {
        event.stopPropagation()
        if (window.getSelection()?.toString()) return
        setOpen(v => !v)
      }}
    >
      <div className="w-full text-left px-2.5 py-1.5">
        <div className="grid grid-cols-[auto_minmax(min-content,max-content)_minmax(0,1fr)_auto] items-start gap-x-1.5 gap-y-0.5 min-w-0">
          <span className={`w-1.5 h-1.5 rounded-full shrink-0 mt-[5px] ${statusDot}`} />
          <span className="theme-text-primary font-semibold min-w-0 whitespace-nowrap">{labelSummary}</span>
          {preview && <span className="theme-text-muted min-w-0 truncate">({preview})</span>}
          <span className="text-m theme-text-faint shrink-0 justify-self-end">{open ? '▼' : '▶'}</span>
        </div>
      </div>
      {open && (
        <div ref={bodyRef} className="px-2.5 pb-2 space-y-1 min-w-0 overflow-hidden" onClick={(event) => event.stopPropagation()}>
          {entries.map(e => (
            <ToolCallRow key={e.id} entry={e} backgrounded={backgroundedToolIds?.has(e.id)} />
          ))}
        </div>
      )}
    </div>
  )

  return (
    <TimestampRow ts={entries[0]?.ts} showTimestamps={showTimestamps}>
      {content}
    </TimestampRow>
  )
}

export function EntryRow({
  entry,
  onDecidePermission,
  onRetry,
  searchQuery,
  backgroundedToolIds,
  showTimestamps,
}: {
  entry: Entry
  onDecidePermission: (requestId: number, optionId: string, cancelled: boolean) => void
  onRetry?: (entry: Entry) => void
  searchQuery?: string
  backgroundedToolIds?: Set<string>
  showTimestamps?: boolean | 'debug'
}) {
  const content = (() => {
    switch (entry.kind) {
      case 'user': {
        const taskMatch = entry.text.match(/<task-notification>[\s\S]*?<summary>([\s\S]*?)<\/summary>[\s\S]*?<\/task-notification>/)
        if (taskMatch) {
          return <TaskNotificationRow text={entry.text} summary={taskMatch[1].trim()} />
        }
        if (isLocalCommandArtifact(entry.text)) return null
        const ds = entry.deliveryState
        const borderClass = ds === 'failed'
          ? 'border-l-2 theme-border-danger'
          : 'border-l-2 border-accent-yellow/70'
        const opacityClass = ds === 'sending' ? 'opacity-50' : ''
        const lineCount = entry.text.split('\n').length
        const isLong = lineCount > 10
        return (
          <div className={`${opacityClass}`} data-user-entry>
            {isLong ? (
              <CollapsibleUserPrompt text={entry.text} borderClass={borderClass} searchQuery={searchQuery ?? ''} />
            ) : (
              <div
                data-selectable-text
                className={`rounded-md px-2.5 py-2 text-l theme-font-mono theme-text-primary whitespace-pre-wrap break-words leading-snug ${borderClass}`}
                style={{ background: 'var(--theme-chat-message-user-bg)' }}
              >
                <span className="theme-text-warning mr-1">&gt;</span><HighlightText text={entry.text} query={searchQuery} />
              </div>
            )}
            {ds === 'failed' && onRetry && (
              <button
                type="button"
                onClick={() => onRetry(entry)}
                className="ml-2 text-m theme-font-display theme-text-danger theme-hover-text-primary transition-colors"
              >retry</button>
            )}
          </div>
        )
      }
      case 'assistant': {
        if (!entry.text && !entry.thoughts) return null
        if (!entry.text) return null
        return (
          <div data-selectable-text className="text-l theme-font-mono theme-text-primary leading-snug min-w-0 overflow-hidden">
            {entry.thoughts && <ThoughtsDisclosure thoughts={entry.thoughts} />}
            <TypewriterMarkdown text={entry.text} />
          </div>
        )
      }
      case 'tool':
        return <ToolCallRow entry={entry} backgrounded={backgroundedToolIds?.has(entry.id)} />
      case 'system':
        return (
          <div data-selectable-text className="text-m theme-font-mono theme-text-faint italic leading-snug"><HighlightText text={entry.text} query={searchQuery} /></div>
      )
    case 'permission': {
      const t = entry.toolCall
      const order: Record<string, number> = { allow_once: 0, allow_always: 1, reject_once: 2, reject_always: 3 }
      const opts = [...entry.options].sort((a, b) => (order[a.kind] ?? 99) - (order[b.kind] ?? 99))
      const decided = entry.resolved && entry.resolved !== 'pending'
      return (
        <div className="border border-accent-yellow/40 rounded px-2.5 py-1.5 theme-bg-panel-muted">
          <div className="text-l theme-font-mono theme-text-primary mb-1">
            Approve: <span className="text-accent-yellow">{t?.title || t?.kind || 'tool call'}</span>
          </div>
          {t?.locations && t.locations.length > 0 && (
            <div className="text-m theme-font-mono theme-text-faint truncate mb-1">
              {t.locations.map(l => l.path + (l.line ? `:${l.line}` : '')).join(', ')}
            </div>
          )}
          {decided ? (
            <div className={`text-s theme-font-display ${entry.resolved === 'allowed' ? 'text-accent-green' : 'text-accent-red'}`}>
              {entry.resolved === 'allowed' ? '✓ Approved' : '✗ Denied'}
            </div>
          ) : (
            <div className="flex flex-wrap gap-1 mt-1">
              {opts.map(o => {
                const isReject = o.kind.startsWith('reject')
                return (
                  <button
                    key={o.optionId}
                    onClick={() => onDecidePermission(entry.requestId, o.optionId, false)}
                    className={`text-s theme-font-display px-2 py-1 rounded transition-colors ${
                      isReject
                        ? 'bg-accent-red/20 text-accent-red hover:bg-accent-red/30'
                        : 'bg-accent-green/20 text-accent-green hover:bg-accent-green/30'
                    }`}
                    style={{ textShadow: 'var(--theme-text-shadow-pixel)' }}
                  >
                    {o.name || o.kind}
                  </button>
                )
              })}
            </div>
          )}
        </div>
      )
    }
  }
  })()

  if (!content) return null

  return (
    <TimestampRow ts={entry.ts} showTimestamps={showTimestamps} padTop={entry.kind === 'user'}>
      {content}
    </TimestampRow>
  )
}
