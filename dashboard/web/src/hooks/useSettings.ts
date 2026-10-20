import { useState, useEffect, useCallback } from 'react'
import { applyTheme } from '../theme/applyTheme'
import { getTheme } from '../theme/themeRegistry'

export interface DashboardSettings {
  gridRows: number            // grid rows (1-8)
  gridCols: number            // grid columns (2-10)
  defaultCardW: number        // legacy: minimum card width in grid cells
  defaultCardH: number        // legacy: minimum card height in grid cells
  cardsPerRow: number         // target cards per row at reset / new-agent placement
  cardsPerCol: number         // target cards per column at reset / new-agent placement
  cardGap: number             // spacing between cards in px (0 = flush)
  cardPadding: number         // inner padding of each card in px (between border and content)
  agentCardOutputFontSize: number // agent card output/preview font size in px
  chatPanelOutputFontSize: number // chat panel transcript font size in px
  theme: 'fire-red' | 'classic' | 'vscode' | 'vscode-light'
  editorOpenCommand: string    // command template; {path} is replaced with the file path
  browserOpenCommand: string   // command template; {url} is replaced with the URL
  scanlines: boolean          // show GBA scanline overlay
  townDebug: boolean          // overlay walkable-mask grid on the town view
  showTownCard: boolean       // include town as a card in the main grid
  townScale: number           // visual zoom for the town map
  townCellSize: number        // town pathfinding/debug grid cell size in px
  townCellOffsetX: number     // debug/pathfinding grid x offset in px
  townCellOffsetY: number     // debug/pathfinding grid y offset in px
  townCropLeft: number
  townCropTop: number
  townCropRight: number
  townCropBottom: number
}

const DEFAULTS: DashboardSettings = {
  gridRows: 15,
  gridCols: 15,
  defaultCardW: 2,
  defaultCardH: 2,
  cardsPerRow: 3,
  cardsPerCol: 3,
  cardGap: 12,
  cardPadding: 9,
  agentCardOutputFontSize: 10,
  chatPanelOutputFontSize: 13,
  theme: 'fire-red',
  editorOpenCommand: 'code {path}',
  browserOpenCommand: 'open -a "Google Chrome" {url}',
  scanlines: true,
  townDebug: false,
  showTownCard: true,
  townScale: 1,
  townCellSize: 16,
  townCellOffsetX: 0,
  townCellOffsetY: 0,
  townCropLeft: 0,
  townCropTop: 0,
  townCropRight: 544,
  townCropBottom: 480,
}

const STORAGE_KEY = 'pokegents-settings'

const TOWN_CARD_DISABLE_MIGRATION_KEY = 'pokegents-migrated-town-card-off-v1'

function load(): DashboardSettings {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULTS
    const parsed = JSON.parse(raw)
    let migrated = false
    // Town view is expensive in Chrome on large dashboards. Default it off once;
    // users can re-enable it from Settings after this migration flag is set.
    if (!localStorage.getItem(TOWN_CARD_DISABLE_MIGRATION_KEY)) {
      parsed.showTownCard = false
      localStorage.setItem(TOWN_CARD_DISABLE_MIGRATION_KEY, '1')
      migrated = true
    }
    if (typeof parsed.outputFontSize === 'number') {
      // v1 had one ambiguous `outputFontSize`. Keep existing users stable by
      // applying it to both explicit scopes the first time the new settings load.
      if (typeof parsed.agentCardOutputFontSize !== 'number') parsed.agentCardOutputFontSize = parsed.outputFontSize
      if (typeof parsed.chatPanelOutputFontSize !== 'number') parsed.chatPanelOutputFontSize = parsed.outputFontSize
      delete parsed.outputFontSize
      migrated = true
    }
    if (!parsed.editorOpenCommand && parsed.defaultEditor) {
      parsed.editorOpenCommand = parsed.defaultEditor === 'system' ? 'open {path}' : `${parsed.defaultEditor === 'vscode' ? 'code' : parsed.defaultEditor} {path}`
      delete parsed.defaultEditor
      migrated = true
    }
    if (!parsed.browserOpenCommand && parsed.defaultBrowser) {
      const app = parsed.defaultBrowser
      parsed.browserOpenCommand = app === 'chrome'
        ? 'open -a "Google Chrome" {url}'
        : app === 'system'
          ? 'open {url}'
          : `open -a "${String(app).charAt(0).toUpperCase() + String(app).slice(1)}" {url}`
      delete parsed.defaultBrowser
      migrated = true
    }
    delete parsed.autoCollapseMinutes
    if (migrated) localStorage.setItem(STORAGE_KEY, JSON.stringify(parsed))
    return { ...DEFAULTS, ...parsed }
  } catch {
    return DEFAULTS
  }
}

export function useSettings() {
  const [settings, setSettingsState] = useState<DashboardSettings>(load)

  const setSettings = useCallback((update: Partial<DashboardSettings>) => {
    setSettingsState(prev => {
      const next = { ...prev, ...update }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
      return next
    })
  }, [])

  const reset = useCallback(() => {
    localStorage.removeItem(STORAGE_KEY)
    setSettingsState(DEFAULTS)
  }, [])

  // Apply theme class to body
  useEffect(() => {
    applyTheme(getTheme(settings.theme), settings.theme)
    document.body.classList.toggle('no-scanlines', !settings.scanlines)
  }, [settings.theme, settings.scanlines])

  // Apply scoped output font sizes as CSS variables.
  useEffect(() => {
    document.documentElement.style.setProperty('--agent-card-output-font-size', `${settings.agentCardOutputFontSize}px`)
    document.documentElement.style.setProperty('--chat-panel-output-font-size', `${settings.chatPanelOutputFontSize}px`)
    // Back-compat for older surfaces that have not been split into a dedicated scope.
    document.documentElement.style.setProperty('--output-font-size', `${settings.agentCardOutputFontSize}px`)
  }, [settings.agentCardOutputFontSize, settings.chatPanelOutputFontSize])

  // Apply inner card padding as CSS variable (consumed by AgentCard)
  useEffect(() => {
    document.documentElement.style.setProperty('--card-padding', `${settings.cardPadding}px`)
  }, [settings.cardPadding])

  return { settings, setSettings, reset, DEFAULTS }
}
