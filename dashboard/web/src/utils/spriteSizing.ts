import { useEffect, useMemo, useState } from 'react'

export interface SpriteNaturalSize {
  w: number
  h: number
}

const spriteSizeCache = new Map<string, SpriteNaturalSize>()
const listeners = new Map<string, Set<() => void>>()
const loading = new Set<string>()

function notify(sprite: string) {
  const subs = listeners.get(sprite)
  if (!subs) return
  for (const fn of subs) fn()
}

export function preloadSpriteNaturalSize(sprite: string) {
  if (!sprite || spriteSizeCache.has(sprite) || loading.has(sprite)) return
  loading.add(sprite)
  const img = new window.Image()
  img.onload = () => {
    loading.delete(sprite)
    spriteSizeCache.set(sprite, { w: img.naturalWidth, h: img.naturalHeight })
    notify(sprite)
  }
  img.onerror = () => {
    loading.delete(sprite)
    notify(sprite)
  }
  img.src = `/sprites/${sprite}.png`
}

export function rememberSpriteNaturalSize(sprite: string, img: HTMLImageElement) {
  if (!sprite || !img.naturalWidth || !img.naturalHeight) return
  const next = { w: img.naturalWidth, h: img.naturalHeight }
  const prev = spriteSizeCache.get(sprite)
  if (prev?.w === next.w && prev?.h === next.h) return
  spriteSizeCache.set(sprite, next)
  notify(sprite)
}

export function getSpriteNaturalSize(sprite: string | undefined): SpriteNaturalSize | undefined {
  return sprite ? spriteSizeCache.get(sprite) : undefined
}

export function useSpriteNaturalSize(sprite: string | undefined) {
  const [version, setVersion] = useState(0)
  useEffect(() => {
    if (!sprite) return
    preloadSpriteNaturalSize(sprite)
    const set = listeners.get(sprite) ?? new Set<() => void>()
    const bump = () => setVersion(v => v + 1)
    set.add(bump)
    listeners.set(sprite, set)
    return () => {
      set.delete(bump)
      if (set.size === 0) listeners.delete(sprite)
    }
  }, [sprite])
  return useMemo(() => getSpriteNaturalSize(sprite), [sprite, version])
}

export function tinySpriteScaleFor(size: SpriteNaturalSize | undefined, maxArtPx = 18, boost = 2) {
  if (!size) return 1
  return Math.max(size.w, size.h) <= maxArtPx ? boost : 1
}
