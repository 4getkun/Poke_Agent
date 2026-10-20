import { useMemo, useState, type DragEvent, type KeyboardEvent } from 'react'
import type { Snapshot, Task } from './api'
import { hasMessageKey, translate, type Locale } from './i18n'
import { ActivityMap, AssignedSprite, PixelCharacter, spriteForTask } from './ActivityMap'
import { ActiveAgents } from './ActiveAgents'
import './workspace.css'

type WorkspaceSnapshot = Snapshot & {
  settings?: Snapshot['settings'] & { board_order?: string[] }
  previews?: Record<string, { body: string; role: string; created_at: string }>
}

interface TaskWorkspaceProps {
  snapshot: Snapshot
  selectedProjectId: string | null
  onProject: (id: string | null) => void
  selectedTaskId: string | null
  onSelect: (id: string) => void
  onEditAgent: (id: string) => void
  onCreate: () => void
  onCreateProject: () => void
  onCreateRole: () => void
  onReorder: (order: string[]) => Promise<boolean>
  busy: boolean
  locale: Locale
  originalAssets?: boolean
}

type BoardStatus = 'BACKLOG' | 'READY' | 'DOING' | 'REVIEW' | 'DONE' | 'CANCELLED'
type WorkspaceMode = 'desk' | 'board'

const BOARD_STATUSES: BoardStatus[] = ['BACKLOG', 'READY', 'DOING', 'REVIEW', 'DONE']

function statusOf(task: Task): BoardStatus {
  const state = task.state.toLowerCase().replace(/-/g, '_')
  if (state === 'cancelled' || state === 'canceled') return 'CANCELLED'
  if (state === 'done' || state === 'accepted' || state === 'completed') return 'DONE'
  if (state === 'review' || state === 'reviewing') return 'REVIEW'
  if (state === 'ready') return 'READY'
  if (state === 'draft' || state === 'backlog' || state === 'created') return 'BACKLOG'
  return 'DOING'
}

function labels(locale: Locale) {
  return locale === 'ja'
    ? {
        desk: '作業デスク', board: 'タスクボード', search: 'タスクを検索', project: 'プロジェクト',
        allProjects: 'すべてのプロジェクト', role: '役割', allRoles: 'すべての役割',
        create: 'タスクを追加', createProject: 'プロジェクトを登録', createRole: '役割を登録',
        cancelled: '取り消し済みを表示', empty: '条件に合うタスクはありません。', noTasks: 'タスクはまだありません。',
        noProject: 'プロジェクト未設定', noRole: '役割未設定', run: '実行', evidence: '必須証拠',
        approval: '承認待ち', moveUp: '同じ状態内で上へ', moveDown: '同じ状態内で下へ', drag: 'ドラッグして並べ替え',
        backlog: '未整理', ready: '実行待ち', doing: '作業中', review: 'レビュー', done: '完了',
        cancelledTitle: '取り消し済み', taskCount: '件', passed: '提出',
      }
    : {
        desk: 'Work desk', board: 'Task board', search: 'Search tasks', project: 'Project',
        allProjects: 'All projects', role: 'Role', allRoles: 'All roles',
        create: 'Add task (BACKLOG)', createProject: 'Add project', createRole: 'Add role',
        cancelled: 'Show CANCELLED', empty: 'No tasks match these filters.', noTasks: 'No tasks yet.',
        noProject: 'No project', noRole: 'No role', run: 'Run', evidence: 'Required evidence',
        approval: 'Approval pending', moveUp: 'Move up within state', moveDown: 'Move down within state', drag: 'Drag to reorder',
        backlog: 'BACKLOG', ready: 'READY', doing: 'DOING', review: 'REVIEW', done: 'DONE',
        cancelledTitle: 'CANCELLED', taskCount: 'tasks', passed: 'passed',
      }
}

function stateLabel(status: BoardStatus, text: ReturnType<typeof labels>): string {
  if (status === 'CANCELLED') return text.cancelledTitle
  return text[status.toLowerCase() as 'backlog' | 'ready' | 'doing' | 'review' | 'done']
}

export function TaskWorkspace({
  snapshot,
  selectedProjectId,
  onProject,
  selectedTaskId,
  onSelect,
  onEditAgent,
  onCreate,
  onCreateProject,
  onCreateRole,
  onReorder,
  busy,
  locale,
  originalAssets = false,
}: TaskWorkspaceProps) {
  const [mode, setMode] = useState<WorkspaceMode>('desk')
  const [roleFilter, setRoleFilter] = useState('')
  const [query, setQuery] = useState('')
  const [showCancelled, setShowCancelled] = useState(false)
  const [draggedId, setDraggedId] = useState<string | null>(null)
  const [dropId, setDropId] = useState<string | null>(null)
  const text = labels(locale)
  const mapSnapshot = { ...snapshot, tasks: snapshot.tasks.filter((task) => (!selectedProjectId || task.spec.project_id === selectedProjectId) && (!roleFilter || task.spec.role_id === roleFilter) && !['DONE', 'CANCELLED'].includes(task.state)) }
  const data = snapshot as WorkspaceSnapshot
  const projectById = useMemo(() => new Map(snapshot.projects.map((project) => [project.id, project])), [snapshot.projects])
  const roleById = useMemo(() => new Map(snapshot.roles.map((role) => [role.id, role])), [snapshot.roles])
  const taskById = useMemo(() => new Map(snapshot.tasks.map((task) => [task.id, task])), [snapshot.tasks])
  const order = useMemo(() => {
    const known = new Set(snapshot.tasks.map((task) => task.id))
    const saved = data.settings?.board_order ?? []
    return [...saved.filter((id, index) => known.has(id) && saved.indexOf(id) === index), ...snapshot.tasks.map((task) => task.id).filter((id) => !saved.includes(id))]
  }, [data.settings?.board_order, snapshot.tasks])
  const taskPosition = useMemo(() => new Map(order.map((id, index) => [id, index])), [order])
  const filteredTasks = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase(locale === 'ja' ? 'ja-JP' : 'en-US')
    return snapshot.tasks.filter((task) => {
      if (selectedProjectId && task.spec.project_id !== selectedProjectId) return false
      if (roleFilter && (task.spec.role_id ?? '') !== roleFilter) return false
      if (!needle) return true
      const project = projectById.get(task.spec.project_id)?.name ?? ''
      const role = roleById.get(task.spec.role_id ?? '')?.name ?? ''
      return `${task.spec.objective} ${project} ${role} ${task.state}`.toLocaleLowerCase(locale === 'ja' ? 'ja-JP' : 'en-US').includes(needle)
    }).sort((left, right) => (taskPosition.get(left.id) ?? 0) - (taskPosition.get(right.id) ?? 0))
  }, [locale, projectById, query, roleById, roleFilter, selectedProjectId, snapshot.tasks, taskPosition])

  async function reorderVisible(status: BoardStatus, sourceId: string, targetId: string) {
    if (busy || sourceId === targetId) return
    const peers = filteredTasks.filter((task) => statusOf(task) === status).map((task) => task.id)
    const from = peers.indexOf(sourceId)
    const to = peers.indexOf(targetId)
    if (from < 0 || to < 0) return
    peers.splice(from, 1)
    peers.splice(to, 0, sourceId)
    const next = [...order]
    const peerSet = new Set(peers)
    const slots = next.flatMap((id, index) => peerSet.has(id) ? [index] : [])
    slots.forEach((slot, index) => { next[slot] = peers[index] })
    if (next.some((id, index) => id !== order[index])) await onReorder(next)
  }

  function startDrag(event: DragEvent<HTMLButtonElement>, task: Task) {
    if (busy) { event.preventDefault(); return }
    setDraggedId(task.id)
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', task.id)
  }

  async function dropOn(event: DragEvent<HTMLElement>, task: Task) {
    event.preventDefault()
    const sourceId = event.dataTransfer.getData('text/plain') || draggedId
    setDropId(null)
    setDraggedId(null)
    if (!sourceId) return
    const source = taskById.get(sourceId)
    if (!source || statusOf(source) !== statusOf(task)) return
    await reorderVisible(statusOf(task), sourceId, task.id)
  }

  function moveByKeyboard(event: KeyboardEvent<HTMLButtonElement>, task: Task, delta: -1 | 1) {
    event.stopPropagation()
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    const peers = filteredTasks.filter((item) => statusOf(item) === statusOf(task))
    const index = peers.findIndex((item) => item.id === task.id)
    const target = peers[index + delta]
    if (target) void reorderVisible(statusOf(task), task.id, target.id)
  }

  function renderCard(task: Task) {
    const profile = snapshot.settings?.agent_profiles?.[task.id]
    const agentName = profile?.name || (locale === 'ja' ? '名前未設定' : 'Unnamed')
    const project = projectById.get(task.spec.project_id)
    const role = task.spec.role_id ? roleById.get(task.spec.role_id) : undefined
    const run = task.current_run_id ? snapshot.runs.find((item) => item.id === task.current_run_id && item.task_id === task.id) : undefined
    const submission = snapshot.submissions.find((item) => item.submission_id === task.submission_id
      && item.task_id === task.id && item.run_id === task.current_run_id && item.spec_version === task.spec.version)
    const requiredCriteria = task.spec.acceptance_criteria.filter((criterion) => criterion.required)
    const passedCount = requiredCriteria.filter((criterion) => submission?.criterion_evidence?.some((evidence) => evidence.criterion_id === criterion.criterion_id && evidence.passed)).length
    const pendingApproval = !!run && snapshot.approvals.some((approval) => approval.run_id === run.id && approval.generation === run.generation && approval.state.toLowerCase() === 'pending')
    const preview = data.previews?.[task.id]
    const state = statusOf(task)
    const peers = filteredTasks.filter((item) => statusOf(item) === state)
    const peerIndex = peers.findIndex((item) => item.id === task.id)
    const stopCardKey = (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.target !== event.currentTarget) return
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); onSelect(task.id) }
    }

    return (
      <article
        key={task.id}
        className={`win-desk-card ${run && ['starting', 'running'].includes(run.state) ? 'is-running' : ''} ${selectedTaskId === task.id ? 'is-selected' : ''} ${draggedId === task.id ? 'is-dragging' : ''} ${dropId === task.id ? 'is-drop-target' : ''}`}
        role="button"
        tabIndex={0}
        aria-pressed={selectedTaskId === task.id}
        onClick={() => onSelect(task.id)}
        onKeyDown={stopCardKey}
        onDragOver={(event) => {
          if (!draggedId || draggedId === task.id) return
          const source = taskById.get(draggedId)
          if (!source || statusOf(source) !== state) return
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
          setDropId(task.id)
        }}
        onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropId(null) }}
        onDrop={(event) => { void dropOn(event, task) }}
      >
        <div className="win-desk-card__top">
          <div className="win-desk-card__head">
            <strong title={task.spec.objective}>{task.spec.objective || task.id}</strong>
            <span className={`win-desk-state win-desk-state--${state.toLowerCase()}`}>{stateLabel(state, text)}</span>
          </div>
          <button
            type="button"
            className="win-desk-card__drag"
            aria-label={text.drag}
            title={text.drag}
            draggable
            disabled={busy}
            onClick={(event) => event.stopPropagation()}
            onDragStart={(event) => startDrag(event, task)}
            onDragEnd={() => { setDraggedId(null); setDropId(null) }}
          >⠿</button>
        </div>
        <div className="win-desk-card__tags">
          <span className="win-desk-tag win-desk-tag--role" title={role?.name ?? text.noRole}>{role?.name ?? text.noRole}</span>
          <span className="win-desk-tag" title={project?.name ?? text.noProject}>{project?.name ?? text.noProject}</span>
          {run && <span className={`win-desk-tag ${run.state.toLowerCase().includes('running') || run.state.toLowerCase() === 'in_progress' ? 'win-desk-tag--busy' : 'win-desk-tag--run'}`}>{text.run}: {translate(locale, hasMessageKey(`state.${run.state === 'succeeded' ? 'completed' : run.state === 'waiting_approval' ? 'waitingApproval' : run.state}`) ? `state.${run.state === 'succeeded' ? 'completed' : run.state === 'waiting_approval' ? 'waitingApproval' : run.state}` as import('./i18n').MessageKey : 'state.unknown')}</span>}
        </div>
        {preview?.body && <p className="win-desk-card__preview" title={preview.body}>{preview.body}</p>}
        <div className="win-desk-card__footer">
          <div className="win-desk-card__summary">
            <span className="win-desk-evidence">{text.evidence}: {passedCount}/{requiredCriteria.length} {text.passed}</span>
            {pendingApproval && <span className="win-desk-approval">{text.approval}</span>}
          </div>
          <div className="win-desk-card__people">
            <span className="win-desk-card__reorder">
              <button type="button" aria-label={text.moveUp} title={text.moveUp} disabled={busy || peerIndex <= 0} onClick={(event) => { event.stopPropagation(); const target = peers[peerIndex - 1]; if (target) void reorderVisible(state, task.id, target.id) }}>↑</button>
              <button type="button" aria-label={text.moveDown} title={text.moveDown} disabled={busy || peerIndex < 0 || peerIndex >= peers.length - 1} onClick={(event) => { event.stopPropagation(); const target = peers[peerIndex + 1]; if (target) void reorderVisible(state, task.id, target.id) }}>↓</button>
            </span>
            <button type="button" className="win-desk-card__assignee" onClick={(event) => { event.stopPropagation(); onEditAgent(task.id) }} title={`${agentName} · ${role?.name ?? text.noRole}`} aria-label={`${locale === 'ja' ? '名前・ドット絵を変更' : 'Edit name and sprite'}: ${agentName}`}>
              <span className="win-desk-card__assignee-name" title={agentName}>{agentName}</span>
              <span className="win-desk-card__assignee-avatar" aria-hidden="true">
                {originalAssets ? <AssignedSprite taskId={task.id} sprite={profile?.sprite} scale={0.85} /> : <PixelCharacter sprite={spriteForTask(task.id, profile?.sprite)} selected={selectedTaskId === task.id} />}
              </span>
            </button>
          </div>
        </div>
      </article>
    )
  }

  const boardStatuses = showCancelled ? [...BOARD_STATUSES, 'CANCELLED' as const] : BOARD_STATUSES

  return (
    <section className="win-desk" aria-label={locale === 'ja' ? 'タスクワークスペース' : 'Task workspace'}>
      <header className="win-desk__toolbar">
        <div className="win-desk__modes" role="group" aria-label={locale === 'ja' ? '表示形式' : 'View mode'}>
          <button type="button" className={mode === 'desk' ? 'is-active' : ''} aria-pressed={mode === 'desk'} onClick={() => setMode('desk')}>{text.desk}</button>
          <button type="button" className={mode === 'board' ? 'is-active' : ''} aria-pressed={mode === 'board'} onClick={() => setMode('board')}>{text.board}</button>
        </div>
        <span className="win-desk__result-count" aria-live="polite">{filteredTasks.length} {text.taskCount}</span>
        <label className="win-desk-filter win-desk-filter--search">
          <span className="win-desk-sr-only">{text.search}</span>
          <input type="search" placeholder={text.search} value={query} onChange={(event) => setQuery(event.target.value)} />
        </label>
        <div className="win-desk-filter">
          <label htmlFor="win-desk-project">{text.project}</label>
          <select id="win-desk-project" value={selectedProjectId ?? ''} onChange={(event) => onProject(event.target.value || null)}>
            <option value="">{text.allProjects}</option>
            {snapshot.projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
          </select>
          <button type="button" className="win-desk-add" title={text.createProject} aria-label={text.createProject} disabled={busy} onClick={onCreateProject}>＋</button>
        </div>
        <div className="win-desk-filter">
          <label htmlFor="win-desk-role">{text.role}</label>
          <select id="win-desk-role" value={roleFilter} onChange={(event) => setRoleFilter(event.target.value)}>
            <option value="">{text.allRoles}</option>
            {snapshot.roles.map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}
          </select>
          <button type="button" className="win-desk-add" title={text.createRole} aria-label={text.createRole} disabled={busy} onClick={onCreateRole}>＋</button>
        </div>
        <button type="button" className="win-desk-create" disabled={busy} onClick={onCreate}>{text.create}</button>
        {mode === 'board' && <label className="win-desk-cancel-toggle"><input type="checkbox" checked={showCancelled} onChange={(event) => setShowCancelled(event.target.checked)} />{text.cancelled}</label>}
      </header>

      {snapshot.tasks.length === 0 ? (
        <div className="win-desk__surface"><div className="win-desk__map"><ActiveAgents snapshot={snapshot} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} onSelect={onSelect} onEdit={onEditAgent} /><ActivityMap snapshot={mapSnapshot} onSelect={onSelect} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} /></div><div className="win-desk__empty"><p>{text.noTasks}</p><button type="button" disabled={busy} onClick={onCreate}>{text.create}</button></div></div>
      ) : filteredTasks.length === 0 ? (
        <div className="win-desk__empty"><p>{text.empty}</p></div>
      ) : mode === 'desk' ? (
        <div className="win-desk__surface">
          <div className="win-desk__map"><ActiveAgents snapshot={snapshot} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} onSelect={onSelect} onEdit={onEditAgent} /><ActivityMap snapshot={mapSnapshot} onSelect={onSelect} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} /></div>
          <div className="win-desk__grid">{filteredTasks.map(renderCard)}</div>
        </div>
      ) : (
        <div className="win-desk__board" aria-label={text.board}>
          <section className="win-desk-column win-desk-column--map"><header className="win-desk-column__header"><h2>{locale === 'ja' ? '活動マップ' : 'Activity map'}</h2></header><ActiveAgents snapshot={snapshot} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} onSelect={onSelect} onEdit={onEditAgent} /><ActivityMap snapshot={mapSnapshot} onSelect={onSelect} selectedTaskId={selectedTaskId} locale={locale} originalAssets={originalAssets} /></section>
          {boardStatuses.map((status) => {
            const columnTasks = filteredTasks.filter((task) => statusOf(task) === status)
            return (
              <section className={`win-desk-column win-desk-column--${status.toLowerCase()}`} key={status}>
                <header className="win-desk-column__header"><h2>{stateLabel(status, text)}</h2><span>{columnTasks.length} {text.taskCount}</span></header>
                <div className="win-desk-column__cards">
                  {columnTasks.map(renderCard)}
                  {status === 'BACKLOG' && <button type="button" className="win-desk-column__create" disabled={busy} onClick={onCreate}>＋ {text.create}</button>}
                  {columnTasks.length === 0 && status !== 'BACKLOG' && <p className="win-desk-column__empty">0 {text.taskCount}</p>}
                </div>
              </section>
            )
          })}
        </div>
      )}
    </section>
  )
}
