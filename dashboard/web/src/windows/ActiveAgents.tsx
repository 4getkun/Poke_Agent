import { useMemo, useState } from 'react'
import type { Snapshot, Task, Run } from './api'
import type { Locale } from './i18n'
import { activityLabel, AssignedSprite, PixelCharacter, spriteForTask } from './ActivityMap'
import './active-agents.css'

type ProfileSnapshot = Snapshot & { settings?: Snapshot['settings'] & { agent_profiles?: Record<string, { name: string; sprite: string }> } }
const allowed = new Set(['queued', 'starting', 'running', 'waiting_approval', 'cancelling'])
const text = {
  ja: { title: '活動中エージェント', empty: '現在の実行中エージェントはいません', unnamed: '名前未設定', edit: '編集' },
  en: { title: 'Active agents', empty: 'No agents are currently active', unnamed: 'Unnamed', edit: 'Edit' },
} satisfies Record<Locale, Record<string, string>>

export function ActiveAgents({ snapshot, locale, originalAssets = false, selectedTaskId, onSelect, onEdit }: { snapshot: Snapshot; locale: Locale; originalAssets?: boolean; selectedTaskId: string | null; onSelect: (id: string) => void; onEdit: (id: string) => void }) {
  const labels = text[locale]
  const profiles = (snapshot as ProfileSnapshot).settings?.agent_profiles
  const runs = useMemo(() => new Map(snapshot.runs.map((run) => [run.id, run])), [snapshot.runs])
  const active = snapshot.tasks.flatMap((task) => {
    if (!task.current_run_id) return []
    const run = runs.get(task.current_run_id)
    if (!run || run.task_id !== task.id || !allowed.has(run.state.toLowerCase().replace(/-/g, '_'))) return []
    return [{ task, run }]
  })
  return <section className="active-agents" aria-label={`${labels.title} ${active.length}`}>
    <h3>{labels.title} <span>{active.length}</span></h3>
    {active.length === 0 ? <p className="active-agents__empty">{labels.empty}</p> : <ul className="active-agents__list">
      {active.map(({ task, run }) => <AgentRow key={task.id} task={task} run={run} snapshot={snapshot} profile={profiles?.[task.id]} locale={locale} originalAssets={originalAssets} selected={selectedTaskId === task.id} onSelect={onSelect} onEdit={onEdit} labels={labels} />)}
    </ul>}
  </section>
}

function AgentRow({ task, run, snapshot, profile, locale, originalAssets, selected, onSelect, onEdit, labels }: { task: Task; run: Run; snapshot: Snapshot; profile?: { name: string; sprite: string }; locale: Locale; originalAssets: boolean; selected: boolean; onSelect: (id: string) => void; onEdit: (id: string) => void; labels: typeof text[Locale] }) {
  const [imageFailed, setImageFailed] = useState(false)
  const name = profile?.name?.trim() || labels.unnamed
  const sprite = spriteForTask(task.id, profile?.sprite)
  const pending = snapshot.approvals.some((approval) => approval.run_id === run.id && approval.generation === run.generation && approval.state.toLowerCase() === 'pending')
  const status = activityLabel(run, snapshot.previews?.[task.id], pending, locale)
  const taskName = task.spec.objective || task.id
  return <li className={`active-agents__item ${selected ? 'is-selected' : ''}`}>
    <button type="button" className="active-agents__identity" onClick={() => onSelect(task.id)} title={`${name} — ${taskName}`} aria-label={`${name} — ${status} — ${taskName}`} aria-pressed={selected}>
      <span className="active-agents__avatar" aria-hidden="true">{originalAssets && !imageFailed ? <AssignedSprite taskId={task.id} sprite={profile?.sprite} scale={0.9} onError={() => setImageFailed(true)} /> : <PixelCharacter sprite={sprite} selected={selected} />}</span>
      <span className="active-agents__details"><strong>{name}</strong><span className="active-agents__status">{status}</span><span className="active-agents__task" title={taskName}>{taskName}</span></span>
    </button>
    <button type="button" className="active-agents__edit" onClick={() => onEdit(task.id)} title={`${labels.edit}: ${name}`} aria-label={`${labels.edit}: ${name}`}>{labels.edit}</button>
  </li>
}
