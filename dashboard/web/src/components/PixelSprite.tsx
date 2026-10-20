import { CSSProperties, useEffect, useRef } from 'react'
import { useSpriteCenterOffset } from '../utils/spriteCentering'
import { rememberSpriteNaturalSize } from '../utils/spriteSizing'

type SpriteShadow = 'none' | 'panel'

interface PixelSpriteProps {
  sprite: string
  alt?: string
  scale?: number
  shiftY?: number
  /**
   * Visual display scale used only for transparent-bounds centering. Usually
   * this is the same as `scale`; pass explicit axes when the image is sized by
   * CSS width/height instead of transform scale (TownView minimap).
   */
  centerScaleX?: number
  centerScaleY?: number
  flipX?: boolean
  draggable?: boolean
  className?: string
  style?: CSSProperties
  shadow?: SpriteShadow
  onLoad?: (img: HTMLImageElement) => void
}

export function PixelSprite({
  sprite,
  alt = '',
  scale = 1,
  shiftY = 0,
  centerScaleX,
  centerScaleY,
  flipX,
  draggable = false,
  className,
  style,
  shadow = 'none',
  onLoad,
}: PixelSpriteProps) {
  const effectiveCenterScaleX = centerScaleX ?? scale
  const effectiveCenterScaleY = centerScaleY ?? effectiveCenterScaleX
  const { offset, onLoad: onCenterLoad } = useSpriteCenterOffset(sprite, effectiveCenterScaleX, shiftY, effectiveCenterScaleY)
  const imgRef = useRef<HTMLImageElement>(null)

  useEffect(() => {
    const img = imgRef.current
    if (img?.complete && img.naturalWidth) onCenterLoad(img)
  }, [sprite, effectiveCenterScaleX, effectiveCenterScaleY, shiftY])

  const scaleX = flipX ? -scale : scale
  const transform = `translate(${offset.x}px, ${offset.y}px) scale(${scaleX}, ${scale})`
  const filter = shadow === 'panel' ? 'drop-shadow(1px 2px 0 var(--theme-panel-muted-bg))' : undefined

  return (
    <img
      ref={imgRef}
      className={className}
      src={`/sprites/${sprite}.png`}
      alt={alt}
      draggable={draggable}
      onLoad={(e) => {
        onCenterLoad(e.currentTarget)
        rememberSpriteNaturalSize(sprite, e.currentTarget)
        onLoad?.(e.currentTarget)
      }}
      style={{
        imageRendering: 'pixelated',
        maxWidth: 'none',
        maxHeight: 'none',
        userSelect: 'none',
        ...style,
        filter: style?.filter ?? filter,
        transform: style?.transform ? `${transform} ${style.transform}` : transform,
      }}
    />
  )
}
