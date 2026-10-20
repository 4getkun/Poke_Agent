import { useEffect, useState } from 'react'

const cache = new Map<string, { x: number; y: number }>()

export function useSpriteCenterOffset(sprite: string | undefined, scaleX = 1, shiftY = 0, scaleY = scaleX) {
  const key = sprite ? `${sprite}:${scaleX}:${scaleY}` : ''
  const [offset, setOffset] = useState(() => key ? cache.get(key) ?? { x: 0, y: shiftY } : { x: 0, y: shiftY })

  useEffect(() => {
    setOffset(key ? addShift(cache.get(key) ?? { x: 0, y: 0 }, shiftY) : { x: 0, y: shiftY })
  }, [key, shiftY])

  const onLoad = (img: HTMLImageElement) => {
    if (!sprite) return
    const cached = cache.get(key)
    if (cached) { setOffset(addShift(cached, shiftY)); return }
    const next = computeSpriteCenterOffset(img, scaleX, scaleY)
    cache.set(key, next)
    setOffset(addShift(next, shiftY))
  }

  return { offset, onLoad }
}

function addShift(offset: { x: number; y: number }, shiftY: number) {
  return { x: offset.x, y: offset.y + shiftY }
}

export function computeSpriteCenterOffset(img: HTMLImageElement, scaleX: number, scaleY = scaleX): { x: number; y: number } {
  const w = img.naturalWidth
  const h = img.naturalHeight
  if (!w || !h) return { x: 0, y: 0 }

  const canvas = document.createElement('canvas')
  canvas.width = w
  canvas.height = h
  const ctx = canvas.getContext('2d', { willReadFrequently: true })
  if (!ctx) return { x: 0, y: 0 }
  ctx.drawImage(img, 0, 0)

  let minX = w, minY = h, maxX = -1, maxY = -1
  const data = ctx.getImageData(0, 0, w, h).data
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      if (data[(y * w + x) * 4 + 3] > 8) {
        if (x < minX) minX = x
        if (x > maxX) maxX = x
        if (y < minY) minY = y
        if (y > maxY) maxY = y
      }
    }
  }
  if (maxX < 0) return { x: 0, y: 0 }

  const artCenterX = (minX + maxX + 1) / 2
  const artCenterY = (minY + maxY + 1) / 2
  return {
    x: Math.round((w / 2 - artCenterX) * scaleX),
    y: Math.round((h / 2 - artCenterY) * scaleY),
  }
}
