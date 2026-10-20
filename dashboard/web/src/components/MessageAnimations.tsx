import { useEffect, useState, useRef, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { AgentMessage } from '../types'
import { PixelSprite } from './PixelSprite'

// ─── Sprite delivery animation ──────────────────────────────────────────
// The sender's sprite leaves its card, carries a letter to the recipient,
// then scurries back. The sender's real sprite is hidden during transit.

interface DeliveryAnim {
  id: string
  fromSessionId: string
  sprite: string             // sender's sprite name
  fromX: number; fromY: number
  toX: number; toY: number
  startTime: number
}

const DELIVERY_DURATION = 2000 // total ms for out + back

export function useMessageAnimations(
  messages: AgentMessage[],
  cardRefs: React.MutableRefObject<Map<string, HTMLDivElement>>,
  getSpriteForId: (id: string) => string
) {
  const [deliveries, setDeliveries] = useState<DeliveryAnim[]>([])
  const [hiddenSprites, setHiddenSprites] = useState<Set<string>>(new Set())
  const [readingAgents, setReadingAgents] = useState<Set<string>>(new Set())
  const seenIdsRef = useRef<Set<string>>(new Set())
  const seenDeliveredRef = useRef<Set<string>>(new Set())
  const readyRef = useRef(false)
  const getSpriteForSession = useCallback((sessionId: string) => {
    return getSpriteForId(sessionId)
  }, [getSpriteForId])

  // Activate after mount. We no longer backfill message history for the removed
  // bottom mail log, so live SSE messages should animate immediately after the
  // initial dashboard settle period.
  useEffect(() => {
    const timeout = setTimeout(() => { readyRef.current = true }, 1000)
    return () => clearTimeout(timeout)
  }, [])

  useEffect(() => {
    if (!readyRef.current) return

    const newMessages: AgentMessage[] = []
    const newlyRead: AgentMessage[] = []

    for (const m of messages) {
      if (!seenIdsRef.current.has(m.id)) {
        newMessages.push(m)
        seenIdsRef.current.add(m.id)
      }
      if (m.delivered && !seenDeliveredRef.current.has(m.id)) {
        newlyRead.push(m)
        seenDeliveredRef.current.add(m.id)
      }
    }

    // Queue sprite delivery animations
    if (newMessages.length > 0) {
      const anims: DeliveryAnim[] = []
      for (let i = 0; i < newMessages.length; i++) {
        const msg = newMessages[i]
        const fromEl = cardRefs.current.get(msg.from)
        const toEl = cardRefs.current.get(msg.to)
        if (fromEl && toEl) {
          const fromRect = fromEl.getBoundingClientRect()
          const toRect = toEl.getBoundingClientRect()
          anims.push({
            id: msg.id,
            fromSessionId: msg.from,
            sprite: getSpriteForSession(msg.from),
            fromX: fromRect.x + 24,
            fromY: fromRect.y + 24,
            toX: toRect.x + 24,
            toY: toRect.y + 24,
            startTime: Date.now() + i * (DELIVERY_DURATION + 200),
          })
        }
      }
      if (anims.length > 0) {
        setDeliveries(prev => [...prev, ...anims])
        // Hide sender sprites during their animation
        const toHide = new Set(anims.map(a => a.fromSessionId))
        setHiddenSprites(prev => new Set([...prev, ...toHide]))
      }
    }

    // Reading animations
    if (newlyRead.length > 0) {
      const ids = new Set(newlyRead.map(m => m.to))
      setReadingAgents(prev => new Set([...prev, ...ids]))
      setTimeout(() => {
        setReadingAgents(prev => {
          const next = new Set(prev)
          ids.forEach(id => next.delete(id))
          return next
        })
      }, 1000)
    }
  }, [messages, cardRefs, getSpriteForSession])

  // Clean up finished animations and unhide sprites
  useEffect(() => {
    if (deliveries.length === 0) return
    const timer = setInterval(() => {
      const now = Date.now()
      const finished = deliveries.filter(d => now - d.startTime >= DELIVERY_DURATION)
      if (finished.length > 0) {
        const finishedSessions = new Set(finished.map(d => d.fromSessionId))
        // Only unhide if no other active delivery uses that sprite
        setDeliveries(prev => {
          const remaining = prev.filter(d => now - d.startTime < DELIVERY_DURATION)
          const stillActive = new Set(remaining.map(d => d.fromSessionId))
          setHiddenSprites(prev => {
            const next = new Set(prev)
            finishedSessions.forEach(s => { if (!stillActive.has(s)) next.delete(s) })
            return next
          })
          return remaining
        })
      }
    }, 100)
    return () => clearInterval(timer)
  }, [deliveries.length]) // eslint-disable-line react-hooks/exhaustive-deps

  // Inject fake deliveries — every agent sends to a random other agent
  const triggerTestDelivery = useCallback(() => {
    const entries = Array.from(cardRefs.current.entries())
    if (entries.length < 2) return
    const anims: DeliveryAnim[] = []
    const toHide = new Set<string>()
    for (let i = 0; i < entries.length; i++) {
      const [fromSid, fromEl] = entries[i]
      let toIdx = Math.floor(Math.random() * entries.length)
      while (toIdx === i) toIdx = Math.floor(Math.random() * entries.length)
      const [, toEl] = entries[toIdx]
      const fromRect = fromEl.getBoundingClientRect()
      const toRect = toEl.getBoundingClientRect()
      anims.push({
        id: `test-${Date.now()}-${i}`,
        fromSessionId: fromSid,
        sprite: getSpriteForSession(fromSid),
        fromX: fromRect.x + 24, fromY: fromRect.y + 24,
        toX: toRect.x + 24, toY: toRect.y + 24,
        startTime: Date.now() + i * 150,
      })
      toHide.add(fromSid)
    }
    setDeliveries(prev => [...prev, ...anims])
    setHiddenSprites(prev => new Set([...prev, ...toHide]))
  }, [cardRefs, getSpriteForSession])

  return { deliveries, hiddenSprites, readingAgents, triggerTestDelivery }
}

// ─── Overlay: renders traveling sprites ─────────────────────────────────

export function DeliveryOverlay({ deliveries }: { deliveries: DeliveryAnim[] }) {
  const [, forceUpdate] = useState(0)

  useEffect(() => {
    if (deliveries.length === 0) return
    const raf = setInterval(() => forceUpdate(n => n + 1), 16)
    return () => clearInterval(raf)
  }, [deliveries.length])

  if (deliveries.length === 0) return null

  return createPortal(
    <div className="fixed inset-0 pointer-events-none" style={{ zIndex: 10001 }}>
      {deliveries.map(d => {
        const elapsed = Date.now() - d.startTime
        if (elapsed < 0) return null // staggered, not started yet

        const half = DELIVERY_DURATION / 2
        const goingOut = elapsed < half
        const t = goingOut
          ? Math.min(elapsed / half, 1)
          : Math.min((elapsed - half) / half, 1)

        // Ease: cubic out for going, cubic in for return
        const easeOut = 1 - Math.pow(1 - t, 3)
        const easeIn = Math.pow(t, 2)
        const progress = goingOut ? easeOut : easeIn

        // Position: out = from→to, back = to→from
        const startX = goingOut ? d.fromX : d.toX
        const startY = goingOut ? d.fromY : d.toY
        const endX = goingOut ? d.toX : d.fromX
        const endY = goingOut ? d.toY : d.fromY

        const x = startX + (endX - startX) * progress
        const arcHeight = -40 * Math.sin(progress * Math.PI)
        const y = startY + (endY - startY) * progress + arcHeight

        // Flip sprite based on direction
        const movingRight = endX > startX
        const scaleX = movingRight ? -1 : 1

        // Fade at endpoints
        const opacity = goingOut
          ? (t < 0.1 ? t * 10 : 1)
          : (t > 0.9 ? (1 - t) * 10 : 1)

        // Show letter only on the outbound trip
        const showLetter = goingOut

        return (
          <div
            key={d.id + (goingOut ? '-out' : '-back')}
            style={{ position: 'fixed', left: x, top: y, width: 32, height: 32, opacity, transform: 'translate(-50%, -50%)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}
          >
            <PixelSprite sprite={d.sprite} alt="" flipX={movingRight} />
            {showLetter && (
              <img
                src="/sprites/_air-mail.png"
                alt=""
                style={{
                  position: 'absolute',
                  top: -8,
                  left: movingRight ? 16 : -12,
                  width: 28,
                  height: 28,
                  imageRendering: 'pixelated',
                }}
              />
            )}
          </div>
        )
      })}
    </div>,
    document.body
  )
}

// ─── Reading letter indicator ───────────────────────────────────────────

export function ReadingIndicator({ isReading }: { isReading: boolean }) {
  if (!isReading) return null
  return (
    <div className="absolute -top-1 -right-1 z-10 animate-bounce-read">
      <img
        src="/sprites/_letter.png"
        alt=""
        style={{ width: 16, height: 16, imageRendering: 'pixelated' }}
      />
    </div>
  )
}

// ─── Floating bubble system (reusable) ──────────────────────────────────

function BubbleShell({ children, phase, top = -16 }: {
  children: React.ReactNode
  phase: 'open' | 'closed'
  top?: number
}) {
  return (
    <div
      className={`absolute z-10 flex justify-center ${
        phase === 'open' ? 'opacity-100' : 'opacity-0 pointer-events-none'
      }`}
      style={{ top, left: 0, right: 0, transition: 'opacity 0.3s ease-out' }}
    >
      <div
        className="relative rounded-full px-1.5 py-0.5"
        style={{ background: 'var(--theme-dialog-bg)', color: 'var(--theme-dialog-text)', boxShadow: 'var(--theme-text-shadow-pixel)' }}
      >
        <span className="flex items-center justify-center h-[12px] text-l leading-none">{children}</span>
        <div
          className="absolute -bottom-[4px] left-1/2 -translate-x-1/2 w-0 h-0 border-l-[4px] border-l-transparent border-r-[4px] border-r-transparent border-t-[5px]"
          style={{ borderTopColor: 'var(--theme-dialog-bg)' }}
        />
      </div>
    </div>
  )
}

export interface BubbleCycleConfig {
  items: string[]
  showDuration?: number
  pauseMin?: number
  pauseMax?: number
  top?: number
}

export function CyclingBubble({ config, active }: { config: BubbleCycleConfig; active: boolean }) {
  const { items, showDuration = 3000, pauseMin = 2000, pauseMax = 4000, top } = config
  const [phase, setPhase] = useState<'open' | 'closed'>('closed')
  const [idx, setIdx] = useState(() => Math.floor(Math.random() * items.length))
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!active || items.length === 0) {
      setPhase('closed')
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
      return
    }

    function pickNext(prevIdx: number) {
      let next = Math.floor(Math.random() * items.length)
      while (next === prevIdx && items.length > 1) next = Math.floor(Math.random() * items.length)
      return next
    }

    function cycle(currentIdx: number) {
      setIdx(currentIdx)
      setPhase('open')
      timeoutRef.current = setTimeout(() => {
        setPhase('closed')
        const pause = pauseMin + Math.random() * (pauseMax - pauseMin)
        timeoutRef.current = setTimeout(() => {
          cycle(pickNext(currentIdx))
        }, pause)
      }, showDuration + Math.random() * 500)
    }

    const initialDelay = Math.random() * 1500
    timeoutRef.current = setTimeout(() => cycle(idx), initialDelay)
    return () => { if (timeoutRef.current) clearTimeout(timeoutRef.current) }
  }, [active]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!active || items.length === 0) return null
  return <BubbleShell phase={phase} top={top}>{items[idx]}</BubbleShell>
}

// ─── Preset: Busy bubble ────────────────────────────────────────────────

const BUSY_EMOJIS = ['🔧', '🧑‍💻', '💀', '🧠', '🔨', '⚡', '🛠️', '💡', '🔬', '🧪']
const BUSY_CONFIG: BubbleCycleConfig = {
  items: BUSY_EMOJIS,
  showDuration: 3000,
  pauseMin: 2000,
  pauseMax: 4000,
}

export function BusyBubble({ isBusy }: { isBusy: boolean }) {
  return <CyclingBubble config={BUSY_CONFIG} active={isBusy} />
}

// ─── One-shot bubble: shows once on trigger, then disappears ────────────

const DONE_EMOJIS = ['🎉', '✅', '💪', '🏆', '⭐', '🥳', '👏', '🙌']

export function DoneBubble({ isDone }: { isDone: boolean }) {
  const [phase, setPhase] = useState<'open' | 'closed'>('closed')
  const [emoji, setEmoji] = useState('')
  const prevDoneRef = useRef(false)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    // Trigger only on transition to done (not on mount if already done)
    if (isDone && !prevDoneRef.current) {
      setEmoji(DONE_EMOJIS[Math.floor(Math.random() * DONE_EMOJIS.length)])
      setPhase('open')
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
      timeoutRef.current = setTimeout(() => setPhase('closed'), 3000)
    }
    prevDoneRef.current = isDone
    return () => { if (timeoutRef.current) clearTimeout(timeoutRef.current) }
  }, [isDone])

  if (!emoji) return null
  return <BubbleShell phase={phase} top={-16}>{emoji}</BubbleShell>
}
