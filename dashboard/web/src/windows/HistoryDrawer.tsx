import { useEffect, useState } from 'react'
import {
  fetchHistoryInfo,
  importHistory,
  listHistory,
  previewHistory,
  type HistoryInfo,
  type HistoryItem,
  type HistoryPreview,
  type HistorySkipped,
  type Project,
  type Task,
  ApiError,
} from './api'
import { hasMessageKey, type MessageKey } from './i18n'

function id() {
  return crypto.randomUUID()
}

export function HistoryDrawer({ projects, tasks, selectedProjectId, selectedTaskId, t, onImported }: {
  projects: Project[]
  tasks: Task[]
  selectedProjectId: string | null
  selectedTaskId: string | null
  t: (key: MessageKey) => string
  onImported: (taskId: string) => Promise<void>
}) {
  const [info, setInfo] = useState<HistoryInfo | null>(null)
  const [infoError, setInfoError] = useState<MessageKey | null>(null)
  const [projectId, setProjectId] = useState(selectedProjectId ?? '')
  const [taskId, setTaskId] = useState(selectedTaskId ?? '')
  const [archived, setArchived] = useState(false)
  const [items, setItems] = useState<HistoryItem[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [listed, setListed] = useState(false)
  const [preview, setPreview] = useState<HistoryPreview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<MessageKey | null>(null)
  const [result, setResult] = useState<{ imported: number; duplicates: number; skipped: HistorySkipped[]; partial: boolean } | null>(null)

  useEffect(() => {
    let active = true
    void fetchHistoryInfo().then((value) => { if (active) setInfo(value) }).catch((caught: unknown) => {
      if (active) setInfoError(caught instanceof ApiError && hasMessageKey(caught.messageKey) ? caught.messageKey : 'error.generic')
    })
    return () => { active = false }
  }, [])

  const eligibleTasks = tasks.filter((task) => task.spec.project_id === projectId && ['BACKLOG', 'READY'].includes(task.state))
  const selectedTask = eligibleTasks.find((task) => task.id === taskId)
  const chosenProject = projects.find((project) => project.id === projectId)

  function fail(caught: unknown) {
    setError(caught instanceof ApiError && hasMessageKey(caught.messageKey) ? caught.messageKey : 'error.generic')
  }

  async function showHistory(append = false) {
    if (!projectId || busy) return
    setBusy(true); setError(null); setResult(null)
    try {
      const page = await listHistory({ project_id: projectId, archived, ...(append ? { cursor } : {}) })
      setItems((current) => append ? [...current, ...page.items] : page.items)
      setCursor(page.next_cursor)
      setListed(true)
      if (!append) setPreview(null)
    } catch (caught) { fail(caught) } finally { setBusy(false) }
  }

  async function selectHistory(item: HistoryItem) {
    if (!projectId || busy) return
    setBusy(true); setError(null); setPreview(null); setResult(null)
    try { setPreview(await previewHistory({ project_id: projectId, thread_id: item.id })) }
    catch (caught) { fail(caught) } finally { setBusy(false) }
  }

  async function importSelected() {
    if (!selectedTask || !preview || busy) return
    setBusy(true); setError(null); setResult(null)
    try {
      const imported = await importHistory({ request_id: id(), task_id: selectedTask.id, expected_version: selectedTask.version, thread_id: preview.thread.id, preview_sha256: preview.sha256 })
      setResult(imported)
      await onImported(selectedTask.id)
    } catch (caught) { fail(caught) } finally { setBusy(false) }
  }

  return <div className="win-drawer-content">
    <div className="win-panel-header"><h2>{t('history.title')}</h2></div>
    <p className="win-inline-note">{t('history.readOnlyNotice')}</p>
    {infoError && <p className="win-inline-note win-inline-note--error" role="alert">{t(infoError)}</p>}
    {info && <section className="win-evidence">
      <strong>{info.available ? t('history.available') : t('history.unavailable')}</strong>
      <p className="win-evidence__expected">{t('history.runtime')}: {info.runtime_version || t('common.none')}</p>
      <p className="win-evidence__expected">{t('history.executable')}: <code>{info.executable_path || t('common.none')}</code></p>
      <p className="win-evidence__expected">{t('history.home')}: <code>{info.home_path || t('common.none')}</code></p>
      {info.reason && <p className="win-evidence__expected">{info.reason}</p>}
    </section>}
    {info?.available && <>
      <div className="win-form-grid">
        <label className="win-field win-field--wide"><span className="win-field__label">{t('history.project')}</span><select value={projectId} disabled={busy} onChange={(event) => { setProjectId(event.target.value); setItems([]); setPreview(null); setCursor(null); setListed(false); setTaskId(''); setResult(null) }}><option value="">{t('history.chooseProject')}</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name} · {project.display_path}</option>)}</select></label>
        <label className="win-field"><span className="win-field__label">{t('history.task')}</span><select value={taskId} disabled={busy || !projectId} onChange={(event) => setTaskId(event.target.value)}><option value="">{t('history.chooseTask')}</option>{eligibleTasks.map((task) => <option key={task.id} value={task.id}>{task.spec.objective || task.id} · {task.state}</option>)}</select></label>
        <label className="win-field"><span className="win-field__label"><input type="checkbox" checked={archived} disabled={busy} onChange={(event) => { setArchived(event.target.checked); setItems([]); setPreview(null); setListed(false); setCursor(null) }} /> {t('history.archived')}</span></label>
        <button type="button" className="win-button win-button--primary" disabled={busy || !projectId} onClick={() => void showHistory(false)}>{busy ? t('common.loading') : t('history.show')}</button>
      </div>
      {!chosenProject && projectId && <p className="win-inline-note">{t('history.projectChanged')}</p>}
      {error && <p className="win-inline-note win-inline-note--error" role="alert">{t(error)}</p>}
      {result && <section className="win-evidence"><strong>{result.partial ? t('history.partial') : t('history.importComplete')}</strong><p className="win-evidence__expected">{t('history.importedCount')}: {result.imported} · {t('history.duplicateCount')}: {result.duplicates}</p><SkippedList skipped={result.skipped} t={t} /></section>}
      {listed && <section className="win-form-section"><h3>{t('history.results')}</h3>{items.map((item) => <article className="win-evidence" key={item.id}><strong>{item.title || t('history.untitled')}</strong><p className="win-evidence__expected">{item.preview}</p><p className="win-evidence__expected">{item.project_path} · {item.source} · {item.status}</p><button type="button" className="win-button win-button--secondary" disabled={busy} onClick={() => void selectHistory(item)}>{t('history.preview')}</button></article>)}{!items.length && <p className="win-inline-note">{t('history.empty')}</p>}{cursor && <button type="button" className="win-button win-button--secondary" disabled={busy} onClick={() => void showHistory(true)}>{t('history.more')}</button>}</section>}
      {preview && <section className="win-form-section"><h3>{t('history.previewTitle')}: {preview.thread.title || preview.thread.id}</h3>{preview.partial && <p className="win-inline-note win-inline-note--warning">{t('history.partial')}</p>}<p className="win-evidence__expected">{t('history.previewLimit')}</p><p className="win-inline-note">{t('history.timestampFallback')}</p>{preview.messages.slice(0, 50).map((message) => <article className="win-evidence" key={message.key}><div className="win-subheading-row"><strong>{message.role}</strong><time>{message.at || new Date(preview.thread.created_at * 1000).toLocaleString()}</time></div><pre className="win-history-message">{message.body}</pre></article>)}<SkippedList skipped={preview.skipped} t={t} /><button type="button" className="win-button win-button--primary" disabled={busy || !selectedTask} onClick={() => void importSelected()}>{t('history.import')}</button>{!selectedTask && <p className="win-inline-note">{t('history.taskRequired')}</p>}</section>}
    </>}
  </div>
}

function SkippedList({ skipped, t }: { skipped: HistorySkipped[]; t: (key: MessageKey) => string }) {
  if (!skipped.length) return null
  return <div className="win-form-section"><strong>{t('history.skipped')}</strong>{skipped.map((entry, index) => <p className="win-inline-note" key={`${entry.position}-${index}`}>{t('history.position')}: {entry.position} · {entry.kind} · {t((`history.skip.${entry.reason}` in { 'history.skip.unsupported_item': true, 'history.skip.non_text_content': true, 'history.skip.incomplete_turn': true, 'history.skip.missing_id': true, 'history.skip.no_text': true } ? `history.skip.${entry.reason}` : 'history.skip.unknown') as MessageKey)}</p>)}</div>
}
