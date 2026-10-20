import { useEffect, useRef, useState } from 'react'

// ─── Animation Registry ─────────────────────────────────────────────────
// Each animation has a CSS class name, duration, and optional weight (higher = more likely)

export interface SpriteAnimation {
  className: string    // CSS class to apply (defined in pokemon.css)
  duration: number     // how long this animation plays (ms)
  weight?: number      // likelihood weight (default 1)
}

// ─── Animation sets per state ───────────────────────────────────────────
// Each state has a pool of animations to cycle through, plus timing config.

export interface AnimationSetConfig {
  animations: SpriteAnimation[]
  pauseMin: number     // min ms between animations
  pauseMax: number     // max ms between animations
  defaultClass: string // CSS class when no animation is playing (idle pose)
}

// ─── Built-in animation sets ────────────────────────────────────────────

export const BUSY_ANIMATIONS: AnimationSetConfig = {
  defaultClass: 'sprite-idle',
  pauseMin: 0,
  pauseMax: 300,
  animations: [
    { className: 'sprite-hop', duration: 600, weight: 3 },
    { className: 'sprite-bump-right', duration: 500, weight: 2 },
    { className: 'sprite-bump-left', duration: 500, weight: 2 },
    { className: 'sprite-shake', duration: 600, weight: 2 },
    { className: 'sprite-wiggle', duration: 800, weight: 1 },
    { className: 'sprite-nod', duration: 700, weight: 2 },
    { className: 'sprite-jump', duration: 500, weight: 1 },
    { className: 'sprite-lean-forward', duration: 900, weight: 1 },
  ],
}

export const IDLE_ANIMATIONS: AnimationSetConfig = {
  defaultClass: 'sprite-idle-slow',
  pauseMin: 99999,
  pauseMax: 99999,
  animations: [],
}

export const DONE_ANIMATIONS: AnimationSetConfig = {
  defaultClass: 'sprite-idle-slow',
  pauseMin: 99999,
  pauseMax: 99999,
  animations: [],
}

// ─── Hook: cycles through animations from a set ─────────────────────────

function pickWeighted(animations: SpriteAnimation[], lastIdx: number): number {
  const totalWeight = animations.reduce((s, a) => s + (a.weight || 1), 0)
  let r = Math.random() * totalWeight
  for (let i = 0; i < animations.length; i++) {
    r -= (animations[i].weight || 1)
    if (r <= 0) {
      // Avoid repeating the same animation twice
      if (i === lastIdx && animations.length > 1) {
        return (i + 1) % animations.length
      }
      return i
    }
  }
  return 0
}

export function useSpriteAnimation(state: string, active: boolean): string {
  const [animClass, setAnimClass] = useState('')
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastIdxRef = useRef(-1)
  const activeRef = useRef(active)
  activeRef.current = active

  const config = state === 'busy' ? BUSY_ANIMATIONS
    : IDLE_ANIMATIONS

  useEffect(() => {
    if (!active || config.animations.length === 0) {
      setAnimClass(config.defaultClass)
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
      return
    }

    function cycle() {
      if (!activeRef.current) return

      // Pick and play an animation
      const idx = pickWeighted(config.animations, lastIdxRef.current)
      lastIdxRef.current = idx
      const anim = config.animations[idx]
      setAnimClass(anim.className)

      // After animation completes, return to default and schedule next
      timeoutRef.current = setTimeout(() => {
        setAnimClass(config.defaultClass)
        const pause = config.pauseMin + Math.random() * (config.pauseMax - config.pauseMin)
        timeoutRef.current = setTimeout(cycle, pause)
      }, anim.duration)
    }

    // Start after random initial delay
    const initialDelay = Math.random() * 2000
    timeoutRef.current = setTimeout(cycle, initialDelay)

    return () => { if (timeoutRef.current) clearTimeout(timeoutRef.current) }
  }, [active, state]) // eslint-disable-line react-hooks/exhaustive-deps

  return animClass
}
