import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { uploadImage } from '../api'
import { getPromptInputKeyAction, isPromptInputComposing } from './promptInputKeyboard.js'

// Slash commands available in chat mode. Most are dashboard-level commands.
// Codex ACP also parses `/compact` natively, so the dashboard forwards that
// slash command only for Codex-backed chat sessions.
//
// To use full Claude CLI commands, switch the agent to the iTerm2 runtime
// (right-click card → "Switch to iTerm2") — the CLI parses them there.
const SLASH_COMMANDS: { cmd: string; desc: string }[] = [
  { cmd: '/cancel', desc: 'Stop the current turn (ACP session/cancel)' },
  { cmd: '/clear', desc: 'Clear the visible transcript (does not erase JSONL)' },
  { cmd: '/compact', desc: 'Compact Codex conversation history' },
  { cmd: '/exit', desc: 'Shut down this chat session' },
  { cmd: '/model', desc: 'Switch model by exact ID' },
  { cmd: '/effort', desc: 'Set thinking effort: low / medium / high / max' },
  { cmd: '/help', desc: 'Show available chat-mode commands' },
]

// PromptInput is the shared textarea + send affordance used by AgentCard's
// QuickInput and ChatPanel's footer. Handles:
//
//   - Auto-grow (height tracks content, capped by maxHeight prop).
//   - Ctrl+Enter to send; Shift+Enter adds a newline. Plain Enter selects a
//     visible slash completion or adds a newline.
//   - Image paste — detects clipboard image, posts to /api/sessions/{id}/image,
//     embeds `[Image: <path>]` token into the textarea.
//   - Optional inline send button (visible in chat panel; AgentCard hides it
//     because the card grid is too tight).
//
// Routing parity: image upload goes through /api/sessions/{id}/image, which
// works for both runtimes after Phase 5 lands the chat-side handler. Until
// then, image paste from a chat-mode agent still hits the iterm2 endpoint;
// the runtime registry on the backend dispatches based on agent.interface.

export interface PromptInputProps {
  /** Pokegent ID for the upload endpoint. */
  sessionId: string
  /** Sends the prompt. Caller decides which API to call. */
  onSend: (text: string) => void | Promise<void>
  placeholder?: string
  disabled?: boolean
  /** Auto-focus the textarea on mount. */
  autoFocus?: boolean
  /** Show an inline SEND button to the right of the textarea. */
  showSendButton?: boolean
  /** Tailwind classes / inline styles for layout customization. Default styling
   *  matches the GBA-card look in the agent grid. Chat panel passes its own. */
  variant?: 'card' | 'chat'
  maxHeight?: number
  /** Minimum visible lines for the textarea. */
  minLines?: number
  /** Maximum visible lines before the textarea scrolls. */
  maxLines?: number
  isBusy?: boolean
  /** Enables chat-mode slash command autocomplete. Defaults to chat variant only. */
  enableSlashCommands?: boolean
  /** Disables image clipboard uploads while leaving text paste unchanged. */
  disableImageUpload?: boolean
  /** Visible label for the send button when the agent is not busy. */
  sendLabel?: string
  /** Accessible description of the send keyboard shortcut. */
  sendShortcutDescription?: string
}

export function PromptInput({
  sessionId,
  onSend,
  placeholder,
  disabled,
  autoFocus,
  showSendButton,
  variant = 'card',
  maxHeight = 120,
  minLines = variant === 'chat' ? 2 : 1,
  maxLines = 8,
  isBusy,
  enableSlashCommands,
  disableImageUpload = false,
  sendLabel,
  sendShortcutDescription = 'Press Ctrl+Enter to send.',
}: PromptInputProps) {
  const draftKey = `pokegents-draft-${sessionId}`
  const [value, setValue] = useState(() => sessionStorage.getItem(draftKey) || '')
  const [sending, setSending] = useState(false)
  const ref = useRef<HTMLTextAreaElement>(null)
  const isComposingRef = useRef(false)
  const [selectedIdx, setSelectedIdx] = useState(0)
  const draftTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Slash-command autocomplete: active when input starts with `/` and has
  // no spaces yet (i.e. user is still typing the command name).
  const slashCommandsEnabled = enableSlashCommands ?? variant === 'chat'
  const slashPrefix = slashCommandsEnabled && value.startsWith('/') && !value.includes(' ') ? value.toLowerCase() : null
  const completions = useMemo(() => {
    if (!slashPrefix) return []
    return SLASH_COMMANDS.filter(c => c.cmd.startsWith(slashPrefix))
  }, [slashPrefix])
  const showCompletions = completions.length > 0

  // Reset selection when completions list changes.
  useEffect(() => { setSelectedIdx(0) }, [completions.length])

  // Phase 1: debounce 300ms save of draft to sessionStorage.
  useEffect(() => {
    if (draftTimerRef.current) clearTimeout(draftTimerRef.current)
    draftTimerRef.current = setTimeout(() => {
      if (value) {
        sessionStorage.setItem(draftKey, value)
      } else {
        sessionStorage.removeItem(draftKey)
      }
    }, 300)
    return () => { if (draftTimerRef.current) clearTimeout(draftTimerRef.current) }
  }, [value, draftKey])

  useEffect(() => {
    if (autoFocus && !disabled) ref.current?.focus()
  }, [autoFocus, disabled])

  function resizeTextarea() {
    const t = ref.current
    if (!t) return
    t.style.height = 'auto'

    const styles = window.getComputedStyle(t)
    const lineHeight = Number.parseFloat(styles.lineHeight) || 14
    const padding = Number.parseFloat(styles.paddingTop) + Number.parseFloat(styles.paddingBottom)
    const border = Number.parseFloat(styles.borderTopWidth) + Number.parseFloat(styles.borderBottomWidth)
    const minHeight = Math.ceil(lineHeight * minLines + padding + border)
    const lineCap = Math.ceil(lineHeight * maxLines + padding + border)
    const cap = Math.max(1, Math.min(maxHeight, lineCap))
    const nextHeight = Math.max(minHeight, Math.min(t.scrollHeight, cap))

    t.style.height = `${nextHeight}px`
    t.style.overflowY = t.scrollHeight > cap ? 'auto' : 'hidden'
  }

  useLayoutEffect(() => {
    resizeTextarea()
  }, [value, maxHeight, minLines, maxLines])

  async function submit(nativeEvent?: unknown) {
    if (isPromptInputComposing(isComposingRef.current, nativeEvent)) return
    if (!value.trim() || sending || disabled) return
    setSending(true)
    try {
      await onSend(value.trim())
      setValue('')
      sessionStorage.removeItem(draftKey)
    } finally {
      setSending(false)
    }
  }

  async function handlePaste(e: React.ClipboardEvent) {
    if (disableImageUpload) return
    for (const item of e.clipboardData.items) {
      if (item.type.startsWith('image/')) {
        e.preventDefault()
        const blob = item.getAsFile()
        if (!blob) continue
        const result = await uploadImage(sessionId, blob)
        if (result) {
          setValue(prev => prev + (prev && !prev.endsWith(' ') ? ' ' : '') + `[Image: ${result.path}] `)
        }
        return
      }
    }
  }

  function handleKey(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    const action = getPromptInputKeyAction({
      key: e.key,
      ctrlKey: e.ctrlKey,
      shiftKey: e.shiftKey,
      hasCompletions: showCompletions,
      compositionActive: isComposingRef.current,
      nativeEvent: e.nativeEvent,
    })

    if (action === 'send') {
      e.preventDefault()
      void submit(e.nativeEvent)
      return
    }

    // Slash-command autocomplete keeps plain Enter as its selection key.
    if (action === 'complete') {
      e.preventDefault()
      const picked = completions[selectedIdx]
      if (picked) {
        setValue(picked.cmd + ' ')
        setSelectedIdx(0)
      }
      return
    }

    // Slash-command autocomplete navigation.
    if (showCompletions && !isPromptInputComposing(isComposingRef.current, e.nativeEvent)) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelectedIdx(i => (i + 1) % completions.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelectedIdx(i => (i - 1 + completions.length) % completions.length)
        return
      }
      if (e.key === 'Tab') {
        e.preventDefault()
        const picked = completions[selectedIdx]
        if (picked) {
          setValue(picked.cmd + ' ')
          setSelectedIdx(0)
        }
        return
      }
      if (e.key === 'Escape') {
        // Clear the slash prefix so dropdown closes.
        setValue('')
        return
      }
    }
  }

  function handleInput() {
    resizeTextarea()
  }

  const completionMenu = showCompletions && (
    <div
      className="absolute bottom-full left-0 right-0 mb-1 overflow-hidden z-30 gba-dropdown-panel"
      style={{
        maxHeight: 220,
        overflowY: 'auto',
      }}
    >
      {completions.map((c, i) => (
        <button
          key={c.cmd}
          type="button"
          onMouseDown={(e) => {
            e.preventDefault()
            setValue(c.cmd + ' ')
            setSelectedIdx(0)
            ref.current?.focus()
          }}
          onMouseEnter={() => setSelectedIdx(i)}
          className={`w-full text-left px-3 py-1.5 flex items-baseline gap-2 transition-colors ${i === selectedIdx ? 'theme-bg-dropdown-active' : 'theme-bg-dropdown-hover'}`}
        >
          <span className="text-m theme-font-mono theme-text-warning font-semibold shrink-0">{c.cmd}</span>
          <span className="text-m theme-font-body theme-text-primary truncate">{c.desc}</span>
        </button>
      ))}
    </div>
  )

  // Card variant: compact GBA-dialog styling, no send button.
  if (variant === 'card') {
    return (
      <form
        onSubmit={(e) => { e.preventDefault(); e.stopPropagation(); void submit(e.nativeEvent) }}
        onClick={(e) => e.stopPropagation()}
        data-no-drag
        className="relative mt-1 shrink-0"
      >
        {completionMenu}
        <textarea
          ref={ref}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onPaste={handlePaste}
          onKeyDown={handleKey}
          onCompositionStart={() => { isComposingRef.current = true }}
          onCompositionEnd={() => { isComposingRef.current = false }}
          onInput={handleInput}
          rows={minLines}
          placeholder={placeholder ?? 'What will you do?'}
          disabled={disabled}
          aria-description={sendShortcutDescription}
          aria-keyshortcuts="Control+Enter"
          className="w-full gba-dialog-dark text-m leading-snug theme-font-mono rounded-md px-2.5 py-1 theme-placeholder-input outline-none focus:border-accent-blue transition-colors resize-none box-border disabled:opacity-70"
          style={{ minHeight: 22, maxHeight, fontSize: 'var(--agent-card-output-font-size, 10px)' }}
        />
      </form>
    )
  }

  // Chat variant: white GBA-dialog styling (matching card input) with
  // slash-command autocomplete and optional send button.
  return (
    <form
      onSubmit={(e) => { e.preventDefault(); void submit(e.nativeEvent) }}
      className="relative flex items-center gap-1.5 p-2 border-t theme-border-subtle shrink-0"
    >
      {completionMenu}
      <textarea
        ref={ref}
        rows={minLines}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onPaste={handlePaste}
        onKeyDown={handleKey}
        onCompositionStart={() => { isComposingRef.current = true }}
        onCompositionEnd={() => { isComposingRef.current = false }}
        onInput={handleInput}
        placeholder={isBusy ? 'Agent is busy. Messages will be added to queue.' : (placeholder ?? 'What will you do?')}
        disabled={disabled}
        aria-description={sendShortcutDescription}
        aria-keyshortcuts="Control+Enter"
        className={`flex-1 min-w-0 gba-dialog-dark text-m leading-snug theme-font-mono rounded-md px-2.5 py-1 theme-placeholder-input outline-none transition-colors resize-none box-border disabled:opacity-70 ${isBusy ? 'border-accent-red/50 focus:border-accent-red/70' : 'focus:border-accent-blue'}`}
        style={{ maxHeight }}
      />
      {showSendButton && (
        <button
          type="submit"
          disabled={disabled || !value.trim()}
          className={`self-center text-s theme-font-display px-3 py-1.5 transition-colors disabled:opacity-50 ${isBusy ? 'gba-button-red opacity-100' : 'gba-button'}`}
        >{isBusy ? 'QUEUE' : (sendLabel ?? 'SEND')}</button>
      )}
    </form>
  )
}
