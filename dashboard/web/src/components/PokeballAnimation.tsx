import { useState, useEffect, useRef, useCallback } from 'react'

// ── Types ──────────────────────────────────────────────────

export interface PokeballAnim {
  id: string
  type: 'recall' | 'deploy'
  sprite: string
  cardX: number
  cardY: number
  cardW: number
  cardH: number
  bubbleX: number
  bubbleY: number
  // For recall: exact sprite center (so animation aligns with the sprite in the card)
  spriteCx?: number
  spriteCy?: number
  // For deploy: mount the card before animation ends so white fades over it
  onMountCard?: () => void
}

interface Props {
  animations: PokeballAnim[]
  onComplete: (id: string) => void
}

// ── Animation Layer ────────────────────────────────────────

export function PokeballAnimationLayer({ animations, onComplete }: Props) {
  return (
    <div className="fixed inset-0 pointer-events-none z-[100]">
      {animations.map(anim => (
        <SingleAnimation key={anim.id} anim={anim} onComplete={() => onComplete(anim.id)} />
      ))}
    </div>
  )
}

function SingleAnimation({ anim, onComplete }: { anim: PokeballAnim; onComplete: () => void }) {
  if (anim.type === 'recall') return <RecallAnimation anim={anim} onComplete={onComplete} />
  return <DeployAnimation anim={anim} onComplete={onComplete} />
}

// ── Recall: card → red beam → pokéball flies to bubble ─────
// Timeline: beam(400ms) → fly(800ms) → done
// Total: ~1200ms

function RecallAnimation({ anim, onComplete }: { anim: PokeballAnim; onComplete: () => void }) {
  const [phase, setPhase] = useState<'beam' | 'fly' | 'done'>('beam')

  useEffect(() => {
    const t1 = setTimeout(() => setPhase('fly'), 400)
    const t2 = setTimeout(() => { setPhase('done'); onComplete() }, 1200)
    return () => { clearTimeout(t1); clearTimeout(t2) }
  }, [])

  if (phase === 'done') return null

  // Use actual sprite center if provided, otherwise estimate
  const cx = anim.spriteCx ?? (anim.cardX + anim.cardW - 40)
  const cy = anim.spriteCy ?? (anim.cardY + 32)

  return (
    <>
      {phase === 'beam' && (
        <div style={{ position: 'fixed', left: cx, top: cy, transform: 'translate(-50%, -50%)' }}>
          {/* Red energy glow */}
          <div style={{
            width: 80, height: 80,
            borderRadius: '50%',
            background: 'radial-gradient(circle, var(--theme-accent-red) 0%, transparent 70%)',
            animation: 'recallPulse 400ms ease-in forwards',
          }} />
          {/* Sprite shrinking into beam */}
          <img
            src={`/sprites/${anim.sprite}.png`}
            alt=""
            style={{
              position: 'absolute', top: '50%', left: '50%',
              imageRendering: 'pixelated',
              animation: 'recallShrink 400ms ease-in forwards',
            }}
          />
        </div>
      )}

      {phase === 'fly' && (
        <img
          src="/sprites/pokeball.png"
          alt=""
          style={{
            position: 'fixed', width: 28, height: 28,
            imageRendering: 'pixelated',
            filter: 'drop-shadow(0 2px 6px var(--theme-panel-muted-bg))',
            '--sx': `${cx - 14}px`, '--sy': `${cy - 14}px`,
            '--ex': `${anim.bubbleX - 14}px`, '--ey': `${anim.bubbleY - 14}px`,
            '--my': `${Math.min(cy, anim.bubbleY) - 60}px`,
            animation: 'agent-orbFly 800ms cubic-bezier(0.3, 0, 0.2, 1) forwards',
          } as React.CSSProperties}
        />
      )}
    </>
  )
}

// ── Deploy: pokéball flies → single bounce → pops open mid-air ─
// Timeline: fly(800ms) → bounce+morph(800ms) → done
// Total: ~1600ms

function DeployAnimation({ anim, onComplete }: { anim: PokeballAnim; onComplete: () => void }) {
  const [phase, setPhase] = useState<'fly' | 'bounce' | 'done'>('fly')

  useEffect(() => {
    const t1 = setTimeout(() => setPhase('bounce'), 800)
    // Mount card when white circle has fully expanded to rectangle (250ms into bounce)
    const t2 = setTimeout(() => { anim.onMountCard?.() }, 800 + 250)
    // Animation ends after circle expand (250ms) + rectangle fade (250ms)
    const t3 = setTimeout(() => { setPhase('done'); onComplete() }, 800 + 500)
    return () => { clearTimeout(t1); clearTimeout(t2); clearTimeout(t3) }
  }, [])

  if (phase === 'done') return null

  const cx = anim.cardX + anim.cardW / 2
  const cy = anim.cardY + anim.cardH / 2

  return (
    <>
      {/* Phase 1: Agent orb flies from bubble to card center */}
      {phase === 'fly' && (
        <img
          src="/sprites/pokeball.png"
          alt=""
          style={{
            position: 'fixed', width: 28, height: 28,
            imageRendering: 'pixelated',
            filter: 'drop-shadow(0 2px 6px var(--theme-panel-muted-bg))',
            '--sx': `${anim.bubbleX - 14}px`, '--sy': `${anim.bubbleY - 14}px`,
            '--ex': `${cx - 14}px`, '--ey': `${cy - 14}px`,
            '--my': `${Math.min(cy, anim.bubbleY) - 80}px`,
            animation: 'agent-orbFly 800ms cubic-bezier(0.3, 0, 0.2, 1) forwards',
          } as React.CSSProperties}
        />
      )}

      {/* Phase 2: Bounce → white circle morphs into white card shape → fades */}
      {phase === 'bounce' && (
        <>
          {/* Agent orb bounces up once then fades */}
          <div style={{ position: 'fixed', left: cx, top: cy, transform: 'translate(-50%, -50%)' }}>
            <img
              src="/sprites/pokeball.png"
              alt=""
              style={{
                width: 28, height: 28,
                imageRendering: 'pixelated',
                filter: 'drop-shadow(0 2px 6px var(--theme-panel-muted-bg))',
                animation: 'agent-orbPopOpen 400ms ease-out forwards',
              }}
            />
          </div>
          {/* White shape: starts as circle at ball position, morphs to full card rect */}
          <div style={{
            position: 'fixed',
            left: anim.cardX,
            top: anim.cardY,
            width: anim.cardW,
            height: anim.cardH,
            borderRadius: 8,
            background: 'white',
            WebkitMaskImage: 'linear-gradient(to right, transparent, white 30px, white calc(100% - 30px), transparent), linear-gradient(to bottom, transparent, white 30px, white calc(100% - 30px), transparent)',
            WebkitMaskComposite: 'destination-in',
            maskImage: 'linear-gradient(to right, transparent, white 30px, white calc(100% - 30px), transparent), linear-gradient(to bottom, transparent, white 30px, white calc(100% - 30px), transparent)',
            maskComposite: 'intersect',
            animation: 'deployCardMorph 500ms cubic-bezier(0.2, 0, 0, 1) forwards',
            '--cx': `${cx - anim.cardX}px`,
            '--cy': `${cy - anim.cardY}px`,
          } as React.CSSProperties} />
        </>
      )}
    </>
  )
}

// ── Hook ───────────────────────────────────────────────────

export function usePokeballAnimations() {
  const [animations, setAnimations] = useState<PokeballAnim[]>([])
  const pendingCallbacks = useRef<Map<string, () => void>>(new Map())

  const triggerRecall = useCallback((
    sessionId: string,
    sprite: string,
    cardRect: DOMRect,
    bubbleTarget: { x: number; y: number },
    onDone: () => void,
    spritePos?: { spriteCx: number; spriteCy: number },
  ) => {
    const id = `recall-${sessionId}-${Date.now()}`
    pendingCallbacks.current.set(id, onDone)
    setAnimations(prev => [...prev, {
      id, type: 'recall', sprite,
      cardX: cardRect.left, cardY: cardRect.top,
      cardW: cardRect.width, cardH: cardRect.height,
      bubbleX: bubbleTarget.x, bubbleY: bubbleTarget.y,
      spriteCx: spritePos?.spriteCx, spriteCy: spritePos?.spriteCy,
    }])
  }, [])

  const triggerDeploy = useCallback((
    sessionId: string,
    sprite: string,
    bubbleSource: { x: number; y: number },
    cardRect: DOMRect,
    onDone: () => void,
    onMountCard?: () => void,
  ) => {
    const id = `deploy-${sessionId}-${Date.now()}`
    pendingCallbacks.current.set(id, onDone)
    setAnimations(prev => [...prev, {
      id, type: 'deploy', sprite,
      cardX: cardRect.left, cardY: cardRect.top,
      cardW: cardRect.width, cardH: cardRect.height,
      bubbleX: bubbleSource.x, bubbleY: bubbleSource.y,
      onMountCard,
    }])
  }, [])

  const onComplete = useCallback((animId: string) => {
    setAnimations(prev => prev.filter(a => a.id !== animId))
    const cb = pendingCallbacks.current.get(animId)
    if (cb) {
      pendingCallbacks.current.delete(animId)
      cb()
    }
  }, [])

  return { animations, triggerRecall, triggerDeploy, onComplete }
}
