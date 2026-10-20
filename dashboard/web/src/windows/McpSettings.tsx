import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchMcpSettings, updateMcpSettings, type McpScope, type McpSettings } from './api'
import type { MessageKey } from './i18n'

function requestId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const bytes = new Uint8Array(16)
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') crypto.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export function McpSettings({ t }: { t: (key: MessageKey) => string }) {
  const [settings, setSettings] = useState<McpSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<MessageKey | null>(null)
  const [status, setStatus] = useState<MessageKey | null>(null)
  const [copyState, setCopyState] = useState<MessageKey | null>(null)
  const promptRef = useRef<HTMLTextAreaElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    setStatus(null)
    setCopyState(null)
    try {
      setSettings(await fetchMcpSettings())
    } catch {
      setSettings(null)
      setError('mcp.loadError')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const update = async (change: { enabled: boolean; scope: McpScope; rotate?: boolean }, success: MessageKey) => {
    if (!settings?.supported || busy) return
    setBusy(true)
    setError(null)
    setStatus(null)
    setCopyState(null)
    try {
      setSettings(await updateMcpSettings({ request_id: requestId(), ...change }))
      setStatus(success)
    } catch {
      setError('mcp.updateError')
    } finally {
      setBusy(false)
    }
  }

  const copyPrompt = async () => {
    const prompt = settings?.registration_prompt
    if (!settings?.enabled || !prompt) return
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard API unavailable')
      await navigator.clipboard.writeText(prompt)
      setCopyState('mcp.copySuccess')
    } catch {
      setCopyState('mcp.copyFailure')
      promptRef.current?.focus()
      promptRef.current?.select()
    }
  }

  return (
    <div className="win-drawer-content win-mcp-settings">
      <div className="win-panel-header"><h2>{t('mcp.title')}</h2></div>
      {loading && <p role="status">{t('mcp.loading')}</p>}
      {error && <p className="win-inline-note win-inline-note--error" role="alert">{t(error)} <button type="button" className="win-button win-button--quiet win-button--small" onClick={() => void load()}>{t('app.resync')}</button></p>}
      {settings && !settings.supported && <p className="win-inline-note" role="status">{t('mcp.unsupported')}</p>}
      {settings?.supported && <>
        <p className="win-inline-note">{t('mcp.localNote')}</p>
      <div className="win-mcp-status" role="status">{settings.enabled ? t('mcp.stateEnabled') : t('mcp.stateDisabled')}</div>
        <div className="win-actions win-actions--wrap">
          <button type="button" className={settings.enabled ? 'win-button win-button--danger' : 'win-button win-button--primary'} disabled={busy} onClick={() => void update({ enabled: !settings.enabled, scope: settings.scope }, 'mcp.enabledUpdated')}>
            {settings.enabled ? t('mcp.disabled') : t('mcp.enabled')}
          </button>
          <button type="button" className="win-button win-button--secondary" disabled={busy || !settings.enabled} onClick={() => void update({ enabled: settings.enabled, scope: settings.scope, rotate: true }, 'mcp.rotated')}>{t('mcp.rotate')}</button>
        </div>
        <label className="win-mcp-field"><span>{t('mcp.scope')}</span><select value={settings.scope} disabled={busy} onChange={(event) => {
          const scope = event.target.value as McpScope
          if (scope === 'read' || scope === 'task-write') void update({ enabled: settings.enabled, scope }, 'mcp.scopeUpdated')
        }}>
          <option value="read">{t('mcp.scopeRead')}</option>
          <option value="task-write">{t('mcp.scopeTaskWrite')}</option>
        </select></label>
        <dl className="win-mcp-paths">
          <div><dt>{t('mcp.connectionPath')}</dt><dd><code>{settings.connection_path}</code></dd></div>
          <div><dt>{t('mcp.executablePath')}</dt><dd><code>{settings.executable_path}</code></dd></div>
        </dl>
        <section className="win-mcp-tools"><h3>{t('mcp.tools')}</h3>{settings.tools.length ? <ul>{settings.tools.map((tool) => <li key={tool}><code>{tool}</code></li>)}</ul> : <p>—</p>}</section>
        {settings.enabled && <section className="win-mcp-prompt">
          <label htmlFor="mcp-registration-prompt">{t('mcp.prompt')}</label>
          <textarea id="mcp-registration-prompt" ref={promptRef} value={settings.registration_prompt} readOnly rows={8} />
          <button type="button" className="win-button win-button--secondary" disabled={!settings.registration_prompt} onClick={() => void copyPrompt()}>{t('mcp.copy')}</button>
          {copyState && <p role="status" className={copyState === 'mcp.copyFailure' ? 'win-inline-note win-inline-note--error' : 'win-inline-note'}>{t(copyState)}</p>}
        </section>}
        <p className="win-inline-note">{t('mcp.permissionNote')}</p>
        {status && <p role="status" className="win-mcp-update-status">{t(status)}</p>}
      </>}
    </div>
  )
}
