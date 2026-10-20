import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type FormEvent,
  type ReactNode,
  type SetStateAction,
} from 'react'
import { Markdown } from '../components/Markdown'
import { PromptInput } from '../components/PromptInput'
import {
  ApiError,
  consumeEvents,
  fetchCapabilities,
  fetchMessages,
  fetchSnapshot,
  postCommand,
  searchMessages,
  pickDirectory,
  openExternalURL,
  openProjectTerminal,
  setApiToken,
  type AgentProfile,
  type AcceptanceCriterion,
  type Approval,
  type ArtifactRef,
  type Capabilities,
  type ContextPacket,
  type Effect,
  type Message,
  type Notification,
  type Project,
  type Role,
  type Run,
  type Snapshot,
  type Submission,
  type SubmissionInput,
  type Task,
  type TaskSpec,
} from './api'
import { AgentIdentityFields, AgentIdentityEditor, newAgentProfile } from './AgentIdentity'
import dialogueFontLicense from '../assets/fonts/mplus1p/OFL.txt?raw'
import cardFontLicense from '../assets/fonts/zenkakugothicnew/OFL.txt?raw'
import headerFontLicense from '../assets/fonts/notosansjp/OFL.txt?raw'
import { TaskWorkspace } from './TaskWorkspace'
import { McpSettings } from './McpSettings'
import { HistoryDrawer } from './HistoryDrawer'
import { hasMessageKey, translate, type Locale, type MessageKey } from './i18n'

type DetailTab = 'chat' | 'contract' | 'context' | 'review'
type Drawer = 'search' | 'history' | 'notifications' | 'diagnostics' | 'mcp' | 'settings' | null
type ConnectionState = 'connecting' | 'live' | 'disconnected'
type Dialog = 'project' | 'role' | 'task' | null

interface WebViewMessageEvent extends MessageEvent {
  data: unknown
}

interface WebViewBridge {
  postMessage(message: string): void
  addEventListener(type: 'message', listener: (event: WebViewMessageEvent) => void): void
  removeEventListener(type: 'message', listener: (event: WebViewMessageEvent) => void): void
}

declare global {
  interface Window {
    chrome?: { webview?: WebViewBridge }
  }
}

interface CriterionDraft {
  criterionId: string
  procedure: string
  expected: string
  required: boolean
}

interface ContractDraft {
  projectId: string
  roleId: string
  objective: string
  allowedScope: string
  outOfScope: string
  approvalProfile: TaskSpec['approval_profile']
  criteria: CriterionDraft[]
}

interface SubmissionDraft {
  summary: string
  evidence: Record<string, { revision: string; procedure: string; result: string; passed: '' | 'yes' | 'no' }>
  artifactRefs: string
  knownLimitations: string
}

interface ContextDraft {
  sourceRunId: string
  verifiedFacts: string
  decisions: string
  openQuestions: string
  nextActions: string
  artifactRefs: string
}

const initialEvidence = (): SubmissionDraft => ({ summary: '', evidence: {}, artifactRefs: '', knownLimitations: '' })

function makeId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function newContractDraft(projectId = ''): ContractDraft {
  return {
    projectId,
    roleId: '',
    objective: '',
    allowedScope: '',
    outOfScope: '',
    approvalProfile: 'read-only',
    criteria: [{ criterionId: makeId(), procedure: '', expected: '', required: true }],
  }
}

function contractDraftFromSpec(spec: TaskSpec): ContractDraft {
  return {
    projectId: spec.project_id,
    roleId: spec.role_id ?? '',
    objective: spec.objective,
    allowedScope: spec.allowed_scope,
    outOfScope: spec.out_of_scope,
    approvalProfile: spec.approval_profile,
    criteria: spec.acceptance_criteria.map((criterion) => ({
      criterionId: criterion.criterion_id,
      procedure: criterion.procedure,
      expected: criterion.expected,
      required: criterion.required,
    })),
  }
}

function contractSpecFromDraft(draft: ContractDraft, version = 1): TaskSpec {
  const result: TaskSpec = {
    project_id: draft.projectId,
    objective: draft.objective.trim(),
    acceptance_criteria: draft.criteria.map((criterion) => ({
      criterion_id: criterion.criterionId,
      procedure: criterion.procedure.trim(),
      expected: criterion.expected.trim(),
      required: criterion.required,
    })),
    allowed_scope: draft.allowedScope.trim(),
    out_of_scope: draft.outOfScope.trim(),
    approval_profile: draft.approvalProfile,
    version,
  }
  if (draft.roleId) result.role_id = draft.roleId
  return result
}

function contractMatchesTask(draft: ContractDraft, spec: TaskSpec): boolean {
  return draft.projectId === spec.project_id
    && draft.roleId === (spec.role_id ?? '')
    && draft.objective.trim() === spec.objective
    && draft.allowedScope.trim() === spec.allowed_scope
    && draft.outOfScope.trim() === spec.out_of_scope
    && draft.approvalProfile === spec.approval_profile
    && draft.criteria.length === spec.acceptance_criteria.length
    && draft.criteria.every((criterion, index) => {
      const saved = spec.acceptance_criteria[index]
      return !!saved && criterion.criterionId === saved.criterion_id
        && criterion.procedure.trim() === saved.procedure
        && criterion.expected.trim() === saved.expected
        && criterion.required === saved.required
    })
}

function submissionMatchesTask(submission: Submission | undefined, task: Task): submission is Submission {
  return !!submission && submission.submission_id === task.submission_id
    && submission.task_id === task.id
    && submission.run_id === task.current_run_id
    && submission.spec_version === task.spec.version
}

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean)
}

function parseDelimitedLines(value: string, columns: number): string[][] | null {
  const parsed = lines(value).map((line) => line.split('|').map((part) => part.trim()))
  if (parsed.some((row) => row.length !== columns || row.some((part) => !part))) return null
  return parsed
}

function artifactsFromText(value: string): ArtifactRef[] | null {
  const rows = parseDelimitedLines(value, 3)
  if (!rows) return null
  return rows.map(([path_or_uri, revision_or_hash, description]) => ({ path_or_uri, revision_or_hash, description }))
}

function contextDraftFromPacket(packet: ContextPacket | undefined, fallbackRunId: string): ContextDraft {
  return {
    sourceRunId: packet?.source_run_id ?? fallbackRunId,
    verifiedFacts: packet?.verified_facts?.map((fact) => `${fact.text}|${fact.source_ref}`).join('\n') ?? '',
    decisions: packet?.decisions?.map((decision) => `${decision.text}|${decision.reason}|${decision.source_ref}`).join('\n') ?? '',
    openQuestions: packet?.open_questions?.join('\n') ?? '',
    nextActions: packet?.next_actions?.join('\n') ?? '',
    artifactRefs: packet?.artifact_refs?.map((artifact) => `${artifact.path_or_uri}|${artifact.revision_or_hash}|${artifact.description}`).join('\n') ?? '',
  }
}

function jsonText(value: unknown): string {
  try {
    const result = JSON.stringify(value, null, 2)
    return result === undefined ? String(value) : result
  } catch {
    return String(value)
  }
}

function keyForError(error: unknown): MessageKey {
  if (error instanceof ApiError && hasMessageKey(error.messageKey)) return error.messageKey
  return 'error.generic'
}

function stateKey(state: string): MessageKey {
  const normalized = state.toLowerCase().replace(/-/g, '_')
  const known: Record<string, MessageKey> = {
    draft: 'state.draft',
    backlog: 'state.draft',
    doing: 'state.doing',
    review: 'state.review',
    done: 'state.done',
    queued: 'state.queued',
    waiting_approval: 'state.waitingApproval',
    cancelling: 'state.cancelling',
    interrupted: 'state.interrupted',
    expired: 'state.expired',
    unknown: 'state.unknown',
    accept: 'state.accepted',
    decline: 'state.declined',
    created: 'state.draft',
    ready: 'state.ready',
    starting: 'state.starting',
    running: 'state.running',
    in_progress: 'state.running',
    completed: 'state.completed',
    succeeded: 'state.completed',
    failed: 'state.failed',
    error: 'state.failed',
    blocked: 'state.blocked',
    cancelled: 'state.cancelled',
    canceled: 'state.cancelled',
    pending: 'state.pending',
    accepted: 'state.accepted',
    declined: 'state.declined',
    returned: 'state.returned',
  }
  return known[normalized] ?? 'state.other'
}

function StatePill({ value, t }: { value: string; t: (key: MessageKey) => string }) {
  const key = stateKey(value)
  return <span className={`win-state-pill win-state-pill--${key.split('.')[1]}`} title={value}>{t(key)}{key === 'state.other' ? ` · ${value}` : ''}</span>
}

function EvidenceResultPill({ passed, t }: { passed: boolean; t: (key: MessageKey) => string }) {
  const key = passed ? 'submission.passed' : 'submission.failed'
  return <span className={`win-state-pill win-state-pill--${passed ? 'accepted' : 'failed'}`}>{t(key)}</span>
}

function Field({ label, hint, children, className = '' }: { label: string; hint?: string; children: ReactNode; className?: string }) {
  return (
    <label className={`win-field ${className}`}>
      <span className="win-field__label">{label}</span>
      {children}
      {hint && <span className="win-field__hint">{hint}</span>}
    </label>
  )
}

function EmptyState({ children }: { children: ReactNode }) {
  return <p className="win-empty">{children}</p>
}

function PanelHeader({ title, action }: { title: string; action?: ReactNode }) {
  return <div className="win-panel-header"><h2>{title}</h2>{action}</div>
}

function ContractFields({
  draft,
  setDraft,
  projects,
  roles,
  t,
}: {
  draft: ContractDraft
  setDraft: Dispatch<SetStateAction<ContractDraft>>
  projects: Project[]
  roles: Role[]
  t: (key: MessageKey) => string
}) {
  function update<K extends keyof ContractDraft>(key: K, value: ContractDraft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  function updateCriterion(index: number, patch: Partial<CriterionDraft>) {
    setDraft((current) => ({
      ...current,
      criteria: current.criteria.map((criterion, itemIndex) => itemIndex === index ? { ...criterion, ...patch } : criterion),
    }))
  }

  return (
    <div className="win-form-grid">
      <Field label={t('task.project')}>
        <select value={draft.projectId} onChange={(event) => update('projectId', event.target.value)}>
          <option value="">{t('task.project')}</option>
          {projects.filter((project) => !project.archived).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
        </select>
      </Field>
      <Field label={t('task.role')}>
        <select value={draft.roleId} onChange={(event) => update('roleId', event.target.value)}>
          <option value="">{t('task.noRole')}</option>
          {roles.map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}
        </select>
      </Field>
      <Field label={t('task.objective')} className="win-field--wide">
        <textarea rows={3} value={draft.objective} onChange={(event) => update('objective', event.target.value)} />
      </Field>
      <Field label={t('task.allowedScope')}>
        <textarea rows={3} value={draft.allowedScope} onChange={(event) => update('allowedScope', event.target.value)} />
      </Field>
      <Field label={t('task.outOfScope')}>
        <textarea rows={3} value={draft.outOfScope} onChange={(event) => update('outOfScope', event.target.value)} />
      </Field>
      <Field label={t('task.approvalProfile')} className="win-field--wide">
        <select value={draft.approvalProfile} onChange={(event) => update('approvalProfile', event.target.value as TaskSpec['approval_profile'])}>
          <option value="read-only">{t('task.profileReadOnly')}</option>
          <option value="workspace-write">{t('task.profileWorkspaceWrite')}</option>
        </select>
        <span className="win-field__hint">{t('task.profileNotice')}</span>
      </Field>
      <div className="win-field win-field--wide">
        <div className="win-subheading-row">
          <span className="win-field__label">{t('task.acceptanceCriteria')}</span>
          <button type="button" className="win-button win-button--quiet" onClick={() => update('criteria', [...draft.criteria, { criterionId: makeId(), procedure: '', expected: '', required: true }])}>{t('task.addCriterion')}</button>
        </div>
        <div className="win-criteria-list">
          {draft.criteria.map((criterion, index) => (
            <fieldset className="win-criterion" key={criterion.criterionId}>
              <legend>{t('task.acceptanceCriteria')} {index + 1}</legend>
              <Field label={t('task.criterionProcedure')}>
                <textarea rows={2} value={criterion.procedure} onChange={(event) => updateCriterion(index, { procedure: event.target.value })} />
              </Field>
              <Field label={t('task.criterionExpected')}>
                <textarea rows={2} value={criterion.expected} onChange={(event) => updateCriterion(index, { expected: event.target.value })} />
              </Field>
              <div className="win-criterion__footer">
                <label className="win-check"><input type="checkbox" checked={criterion.required} onChange={(event) => updateCriterion(index, { required: event.target.checked })} />{t('task.criterionRequired')}</label>
                <button type="button" className="win-button win-button--quiet win-button--danger-text" disabled={draft.criteria.length <= 1} onClick={() => update('criteria', draft.criteria.filter((_, itemIndex) => itemIndex !== index))}>{t('task.removeCriterion')}</button>
              </div>
            </fieldset>
          ))}
        </div>
      </div>
    </div>
  )
}

function ProjectDialog({
  t,
  busy,
  onClose,
  onSubmit,
}: {
  t: (key: MessageKey) => string
  busy: boolean
  onClose: () => void
  onSubmit: (name: string, path: string) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [pickerBusy, setPickerBusy] = useState(false)
  const [pickerError, setPickerError] = useState<MessageKey | null>(null)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    void onSubmit(name.trim(), path.trim())
  }
  async function browseForFolder() {
    setPickerBusy(true)
    setPickerError(null)
    try {
      const result = await pickDirectory()
      if (result.path) setPath(result.path)
    } catch (error) {
      setPickerError(keyForError(error))
    } finally {
      setPickerBusy(false)
    }
  }
  return (
    <Dialog title={t('project.createTitle')} onClose={onClose} t={t}>
      <form onSubmit={submit} className="win-form-grid">
        <Field label={t('project.name')}><input value={name} onChange={(event) => setName(event.target.value)} required autoFocus /></Field>
        <Field label={t('project.path')} hint={t('project.pathHint')}>
          <div className="win-path-picker" style={{ display: 'flex', gap: 7 }}>
            <input style={{ flex: 1 }} value={path} onChange={(event) => setPath(event.target.value)} required />
            <button type="button" className="win-button win-button--secondary" disabled={busy || pickerBusy} onClick={() => void browseForFolder()}>{pickerBusy ? t('common.loading') : t('project.browse')}</button>
          </div>
        </Field>
        {pickerError && <p className="win-inline-note win-inline-note--error" role="alert">{t(pickerError)}</p>}
        <div className="win-dialog-actions"><button type="button" className="win-button win-button--secondary" onClick={onClose}>{t('common.cancel')}</button><button type="submit" className="win-button win-button--primary" disabled={busy || !name.trim() || !path.trim()}>{t('project.create')}</button></div>
      </form>
    </Dialog>
  )
}

function RoleDialog({
  t,
  busy,
  onClose,
  onSubmit,
}: {
  t: (key: MessageKey) => string
  busy: boolean
  onClose: () => void
  onSubmit: (name: string, prompt: string) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [prompt, setPrompt] = useState('')
  const submit = (event: FormEvent) => {
    event.preventDefault()
    void onSubmit(name.trim(), prompt.trim())
  }
  return (
    <Dialog title={t('role.createTitle')} onClose={onClose} t={t}>
      <form onSubmit={submit} className="win-form-grid">
        <Field label={t('role.name')}><input value={name} onChange={(event) => setName(event.target.value)} required autoFocus /></Field>
        <Field label={t('role.prompt')} className="win-field--wide"><textarea rows={6} value={prompt} onChange={(event) => setPrompt(event.target.value)} required /></Field>
        <div className="win-dialog-actions"><button type="button" className="win-button win-button--secondary" onClick={onClose}>{t('common.cancel')}</button><button type="submit" className="win-button win-button--primary" disabled={busy || !name.trim() || !prompt.trim()}>{t('role.create')}</button></div>
      </form>
    </Dialog>
  )
}

function Dialog({ title, onClose, children, t }: { title: string; onClose: () => void; children: ReactNode; t: (key: MessageKey) => string }) {
  const id = `dialog-${makeId()}`
  return (
    <div className="win-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <section className="win-dialog" role="dialog" aria-modal="true" aria-labelledby={id}>
        <div className="win-panel-header"><h2 id={id}>{title}</h2><button type="button" className="win-icon-button" aria-label={t('common.close')} onClick={onClose}>×</button></div>
        {children}
      </section>
    </div>
  )
}

function MessageList({ messages, t }: { messages: Message[]; t: (key: MessageKey) => string }) {
  if (!messages.length) return <EmptyState>{t('chat.empty')}</EmptyState>
  return (
    <div className="win-message-list" aria-live="polite">
      {messages.map((message) => {
        const roleKey = message.role === 'user' ? 'chat.user' : message.role === 'assistant' ? 'chat.assistant' : message.role === 'system' ? 'chat.system' : 'chat.otherRole'
        return (
          <article className={`win-message win-message--${message.role === 'user' ? 'user' : 'agent'}`} key={message.id}>
            <header className="win-message__meta"><strong>{t(roleKey)}</strong><time dateTime={message.created_at}>{formatDate(message.created_at)}</time></header>
            <div className="win-markdown"><Markdown desktop={{ openURL: openExternalURL, imageLabel: t('markdown.imageBlocked'), copyLabel: t('markdown.copy'), copiedLabel: t('markdown.copied') }}>{message.body}</Markdown></div>
          </article>
        )
      })}
    </div>
  )
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function ApprovalCard({
  approval,
  run,
  task,
  t,
  busy,
  onRespond,
}: {
  approval: Approval
  run: Run | undefined
  task: Task | undefined
  t: (key: MessageKey) => string
  busy: boolean
  onRespond: (approval: Approval, run: Run, task: Task, decision: 'accept' | 'decline') => Promise<void>
}) {
  const pending = approval.state.toLowerCase() === 'pending'
  const linked = !!run && !!task && run.task_id === task.id
  return (
    <article className="win-approval">
      <div className="win-subheading-row"><div><h3>{approval.kind}</h3><StatePill value={approval.state} t={t} /></div><span className="win-micro-id">{approval.id}</span></div>
      <p className="win-field__label">{t('approval.request')}</p>
      <pre className="win-json">{jsonText(approval.request)}</pre>
      {!linked && pending && <p className="win-inline-note win-inline-note--warning">{t('approval.unlinked')}</p>}
      {linked && pending && (
        <div className="win-actions">
          {approval.choices.includes('accept') && <button type="button" className="win-button win-button--primary" disabled={busy} onClick={() => void onRespond(approval, run, task, 'accept')}>{t('approval.accept')}</button>}
          {approval.choices.includes('decline') && <button type="button" className="win-button win-button--secondary" disabled={busy} onClick={() => void onRespond(approval, run, task, 'decline')}>{t('approval.decline')}</button>}
        </div>
      )}
      {!pending && <p className="win-inline-note">{t('approval.stale')}</p>}
    </article>
  )
}

function SubmissionForm({
  task,
  run,
  t,
  busy,
  onSubmit,
}: {
  task: Task
  run: Run | undefined
  t: (key: MessageKey) => string
  busy: boolean
  onSubmit: (submission: SubmissionInput) => Promise<void>
}) {
  const [draft, setDraft] = useState<SubmissionDraft>(initialEvidence)
  useEffect(() => setDraft(initialEvidence()), [task.id, task.current_run_id])

  function updateEvidence(id: string, patch: Partial<SubmissionDraft['evidence'][string]>) {
    setDraft((current) => {
      const previous = current.evidence[id]
      return {
        ...current,
        evidence: {
          ...current.evidence,
          [id]: {
            revision: patch.revision ?? previous?.revision ?? '',
            procedure: patch.procedure ?? previous?.procedure ?? '',
            result: patch.result ?? previous?.result ?? '',
            passed: patch.passed ?? previous?.passed ?? '',
          },
        },
      }
    })
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    const evidence = task.spec.acceptance_criteria.map((criterion) => ({ criterion, value: draft.evidence[criterion.criterion_id] }))
    const artifactRefs = artifactsFromText(draft.artifactRefs)
    const completeEvidence = evidence.filter(({ criterion, value }) => {
      const untouched = !value || (!value.revision.trim() && !value.procedure.trim() && !value.result.trim() && value.passed === '')
      return criterion.required || !untouched
    })
    const evidenceInvalid = evidence.some(({ criterion, value }) => {
      const untouched = !value || (!value.revision.trim() && !value.procedure.trim() && !value.result.trim() && value.passed === '')
      if (criterion.required) return untouched || !value?.revision.trim() || !value?.procedure.trim() || !value?.result.trim() || value?.passed !== 'yes'
      return !untouched && (!value?.revision.trim() || !value?.procedure.trim() || !value?.result.trim() || value?.passed === '')
    })
    if (!run || run.task_id !== task.id || run.spec_version !== task.spec.version || !draft.summary.trim() || evidenceInvalid || !artifactRefs) return
    void onSubmit({
      task_id: task.id,
      run_id: run.id,
      spec_version: run.spec_version,
      summary: draft.summary.trim(),
      criterion_evidence: completeEvidence.map(({ criterion, value }) => ({
        criterion_id: criterion.criterion_id,
        revision: value?.revision.trim() ?? '',
        procedure: value?.procedure.trim() ?? '',
        result: value?.result.trim() ?? '',
        passed: value?.passed === 'yes',
      })),
      artifact_refs: artifactRefs,
      known_limitations: lines(draft.knownLimitations),
    })
  }

  const complete = !!run && !!draft.summary.trim() && task.spec.acceptance_criteria.every((criterion) => {
    const value = draft.evidence[criterion.criterion_id]
    const untouched = !value || (!value.revision.trim() && !value.procedure.trim() && !value.result.trim() && value.passed === '')
    if (!criterion.required && untouched) return true
    return !!value?.revision.trim() && !!value?.procedure.trim() && !!value?.result.trim() && (criterion.required ? value.passed === 'yes' : value.passed !== '')
  }) && run.task_id === task.id && run.spec_version === task.spec.version && !!artifactsFromText(draft.artifactRefs)

  return (
    <form className="win-review-form" onSubmit={submit}>
      <h3>{t('submission.formTitle')}</h3>
      <Field label={t('submission.summary')}><textarea rows={3} value={draft.summary} onChange={(event) => setDraft((current) => ({ ...current, summary: event.target.value }))} /></Field>
      <div className="win-form-section"><h4>{t('submission.evidence')}</h4>
        {task.spec.acceptance_criteria.map((criterion) => <SubmissionEvidence key={criterion.criterion_id} criterion={criterion} value={draft.evidence[criterion.criterion_id]} onChange={(patch) => updateEvidence(criterion.criterion_id, patch)} t={t} />)}
      </div>
      <Field label={t('submission.artifacts')} hint={t('submission.artifactFormat')}><textarea rows={2} aria-invalid={!artifactsFromText(draft.artifactRefs)} value={draft.artifactRefs} onChange={(event) => setDraft((current) => ({ ...current, artifactRefs: event.target.value }))} /></Field>
      <Field label={t('submission.limitations')}><textarea rows={2} value={draft.knownLimitations} onChange={(event) => setDraft((current) => ({ ...current, knownLimitations: event.target.value }))} /></Field>
      {!run && <p className="win-inline-note">{t('submission.runRequired')}</p>}
      <button type="submit" className="win-button win-button--primary" disabled={busy || !complete}>{t('submission.send')}</button>
    </form>
  )
}

function SubmissionEvidence({
  criterion,
  value,
  onChange,
  t,
}: {
  criterion: AcceptanceCriterion
  value: SubmissionDraft['evidence'][string] | undefined
  onChange: (patch: Partial<SubmissionDraft['evidence'][string]>) => void
  t: (key: MessageKey) => string
}) {
  return (
    <fieldset className="win-evidence">
      <legend>{criterion.procedure || criterion.criterion_id}{criterion.required ? ` · ${t('common.required')}` : ''}</legend>
      <p className="win-evidence__expected">{criterion.expected}</p>
      <Field label={t('submission.revision')}><input value={value?.revision ?? ''} onChange={(event) => onChange({ revision: event.target.value })} /></Field>
      <Field label={t('submission.procedure')}><textarea rows={2} value={value?.procedure ?? ''} onChange={(event) => onChange({ procedure: event.target.value })} /></Field>
      <Field label={t('submission.result')}><textarea rows={2} value={value?.result ?? ''} onChange={(event) => onChange({ result: event.target.value })} /></Field>
      <Field label={t('submission.chooseResult')}>
        <select value={value?.passed ?? ''} onChange={(event) => onChange({ passed: event.target.value as '' | 'yes' | 'no' })}>
          <option value="">{t('submission.chooseResult')}</option>
          <option value="yes">{t('submission.passed')}</option>
          <option value="no">{t('submission.failed')}</option>
        </select>
      </Field>
    </fieldset>
  )
}

function TaskEvidenceStatus({
  task,
  effects,
  submission,
  t,
  busy,
  onResolveEffect,
  onConfirmRecovery,
}: {
  task: Task
  effects: Effect[]
  submission: Submission | undefined
  t: (key: MessageKey) => string
  busy: boolean
  onResolveEffect: (effect: Effect, outcome: 'succeeded' | 'failed', evidence: string) => Promise<boolean>
  onConfirmRecovery: (reason: string) => Promise<boolean>
}) {
  const currentSubmission = submissionMatchesTask(submission, task) ? submission : undefined
  const evidence = currentSubmission?.criterion_evidence ?? []
  const outcomes = task.spec.acceptance_criteria.map((criterion) => ({
    criterion,
    evidence: evidence.find((item) => item.criterion_id === criterion.criterion_id),
  })).sort((left, right) => Number(left.evidence?.passed === true) - Number(right.evidence?.passed === true))
  const orderedEffects = [...effects].sort((left, right) => {
    const rank = (value: string) => ['pending', 'unknown'].includes(value.toLowerCase()) ? 0 : 1
    return rank(left.outcome) - rank(right.outcome)
  })
  return (
    <section className="win-review-form" aria-label={t('context.statusTitle')}>
      <h3>{t('context.statusTitle')}</h3>
      <div className="win-form-section">
        <h4>{t('context.effects')}</h4>
        {orderedEffects.length ? orderedEffects.map((effect) => <article className="win-evidence" key={effect.effect_id}>
          <div className="win-subheading-row"><strong>{effect.description || effect.effect_id}</strong><StatePill value={effect.outcome} t={t} /></div>
          <p className="win-evidence__expected">{t('context.run')}: {effect.run_id}</p>
          {effect.evidence_ref && <p className="win-evidence__expected">{t('context.evidenceRef')}: {effect.evidence_ref}</p>}
        </article>) : <p className="win-inline-note">{t('context.noEffects')}</p>}
        {!!orderedEffects.some((effect) => ['pending', 'unknown'].includes(effect.outcome.toLowerCase())) && <p className="win-inline-note win-inline-note--warning">{t('context.effectsReadonly')}</p>}
      </div>
      {task.block === 'RECOVERY_REQUIRED' && <RecoveryControls key={`${task.id}:${task.version}`} effects={orderedEffects} busy={busy} t={t} onResolveEffect={onResolveEffect} onConfirmRecovery={onConfirmRecovery} />}
      <div className="win-form-section">
        <h4>{t('context.unfinishedCriteria')}</h4>
        {outcomes.length ? outcomes.map(({ criterion, evidence: result }) => <article className="win-evidence" key={criterion.criterion_id}>
          <div className="win-subheading-row"><strong>{criterion.procedure}</strong>{result ? <EvidenceResultPill passed={result.passed} t={t} /> : <StatePill value="pending" t={t} />}</div>
          <p className="win-evidence__expected">{criterion.expected}</p>
          {!result && <p className="win-evidence__expected">{t('context.notSubmitted')}</p>}
        </article>) : <p className="win-inline-note">{t('context.noCriteria')}</p>}
      </div>
    </section>
  )
}

function RecoveryControls({ effects, busy, t, onResolveEffect, onConfirmRecovery }: {
  effects: Effect[]
  busy: boolean
  t: (key: MessageKey) => string
  onResolveEffect: (effect: Effect, outcome: 'succeeded' | 'failed', evidence: string) => Promise<boolean>
  onConfirmRecovery: (reason: string) => Promise<boolean>
}) {
  const [drafts, setDrafts] = useState<Record<string, { outcome: '' | 'succeeded' | 'failed'; evidence: string }>>({})
  const [reason, setReason] = useState('')
  const unknownEffects = effects.filter((effect) => effect.outcome.toLowerCase() === 'unknown')
  return <div className="win-recovery-actions">
    <h4>{t('recovery.title')}</h4>
    {unknownEffects.map((effect) => {
      const draft = drafts[effect.effect_id] ?? { outcome: '' as const, evidence: '' }
      return <div className="win-evidence" key={effect.effect_id}>
        <strong>{effect.description || effect.effect_id}</strong>
        <Field label={t('recovery.outcome')}><select value={draft.outcome} disabled={busy} onChange={(event) => setDrafts((current) => ({ ...current, [effect.effect_id]: { ...draft, outcome: event.target.value as '' | 'succeeded' | 'failed' } }))}><option value="">{t('recovery.chooseOutcome')}</option><option value="succeeded">{t('recovery.succeeded')}</option><option value="failed">{t('recovery.failed')}</option></select></Field>
        <Field label={t('recovery.evidence')}><textarea rows={2} maxLength={2000} value={draft.evidence} disabled={busy} onChange={(event) => setDrafts((current) => ({ ...current, [effect.effect_id]: { ...draft, evidence: event.target.value } }))} required /></Field>
        <div className="win-actions"><button type="button" className="win-button win-button--secondary" disabled={busy || !draft.outcome || !draft.evidence.trim()} onClick={() => void onResolveEffect(effect, draft.outcome as 'succeeded' | 'failed', draft.evidence.trim())}>{t('recovery.record')}</button></div>
      </div>
    })}
    {!unknownEffects.length && <>
      <p className="win-inline-note">{t('recovery.ownershipNote')}</p>
      <Field label={t('recovery.reason')}><textarea rows={2} value={reason} disabled={busy} onChange={(event) => setReason(event.target.value)} required /></Field>
      <div className="win-actions"><button type="button" className="win-button win-button--primary" disabled={busy || !reason.trim()} onClick={() => void onConfirmRecovery(reason.trim())}>{t('recovery.confirm')}</button></div>
    </>}
  </div>
}

function ContextPacketForm({
  task,
  runs,
  context,
  effects,
  t,
  busy,
  onSave,
}: {
  task: Task
  runs: Run[]
  context: ContextPacket | undefined
  effects: Effect[]
  t: (key: MessageKey) => string
  busy: boolean
  onSave: (packet: ContextPacket) => Promise<void>
}) {
  const currentRun = runs.find((run) => run.id === task.current_run_id)
  const fallbackRunId = currentRun?.id ?? runs[runs.length - 1]?.id ?? ''
  const [draft, setDraft] = useState<ContextDraft>(() => contextDraftFromPacket(context, fallbackRunId))
  useEffect(() => setDraft(contextDraftFromPacket(context, fallbackRunId)), [task.id, task.spec.version, task.current_run_id, context?.spec_version, context?.source_run_id])

  const facts = parseDelimitedLines(draft.verifiedFacts, 2)
  const decisions = parseDelimitedLines(draft.decisions, 3)
  const artifacts = artifactsFromText(draft.artifactRefs)
  const packet: ContextPacket = {
    schema_version: 1,
    task_id: task.id,
    spec_version: task.spec.version,
    source_run_id: draft.sourceRunId,
    objective: task.spec.objective,
    allowed_scope: task.spec.allowed_scope,
    acceptance_criteria: task.spec.acceptance_criteria,
    verified_facts: (facts ?? []).map(([text, source_ref]) => ({ text, source_ref })),
    decisions: (decisions ?? []).map(([text, reason, source_ref]) => ({ text, reason, source_ref })),
    open_questions: lines(draft.openQuestions),
    next_actions: lines(draft.nextActions),
    artifact_refs: artifacts ?? [],
    effects,
  }
  const byteLength = new TextEncoder().encode(JSON.stringify(packet)).byteLength
  const tooLarge = byteLength > 16 * 1024
  const rowsValid = facts !== null && decisions !== null && artifacts !== null
  const sourceRunExists = !draft.sourceRunId || runs.some((run) => run.id === draft.sourceRunId)

  return (
    <section className="win-review-form" aria-label={t('context.title')}>
      <h3>{t('context.title')}</h3>
      <div className="win-task-meta"><span><b>{t('task.version')}</b>{task.spec.version}</span><span><b>{t('context.bytes')}</b>{byteLength} / 16384</span></div>
      <div className="win-form-grid">
        <Field label={t('context.sourceRun')} className="win-field--wide">
          <select value={draft.sourceRunId} onChange={(event) => setDraft((current) => ({ ...current, sourceRunId: event.target.value }))}>
            <option value="">{t('context.noSourceRun')}</option>
            {draft.sourceRunId && sourceRunExists && !runs.some((run) => run.id === draft.sourceRunId) && <option value={draft.sourceRunId}>{draft.sourceRunId}</option>}
            {runs.map((run) => <option value={run.id} key={run.id}>{run.id} · {run.state}</option>)}
          </select>
        </Field>
        <Field label={t('task.objective')}><textarea rows={2} value={task.spec.objective} readOnly /></Field>
        <Field label={t('task.allowedScope')}><textarea rows={2} value={task.spec.allowed_scope} readOnly /></Field>
        <div className="win-field win-field--wide"><span className="win-field__label">{t('task.acceptanceCriteria')}</span>
          {task.spec.acceptance_criteria.map((criterion) => <p className="win-inline-note" key={criterion.criterion_id}>{criterion.procedure} → {criterion.expected}</p>)}
        </div>
        <Field label={t('context.facts')} hint={t('context.factsFormat')} className="win-field--wide"><textarea rows={3} aria-invalid={!facts} value={draft.verifiedFacts} onChange={(event) => setDraft((current) => ({ ...current, verifiedFacts: event.target.value }))} /></Field>
        <Field label={t('context.decisions')} hint={t('context.decisionsFormat')} className="win-field--wide"><textarea rows={3} aria-invalid={!decisions} value={draft.decisions} onChange={(event) => setDraft((current) => ({ ...current, decisions: event.target.value }))} /></Field>
        <Field label={t('context.questions')}><textarea rows={3} value={draft.openQuestions} onChange={(event) => setDraft((current) => ({ ...current, openQuestions: event.target.value }))} /></Field>
        <Field label={t('context.nextActions')}><textarea rows={3} value={draft.nextActions} onChange={(event) => setDraft((current) => ({ ...current, nextActions: event.target.value }))} /></Field>
        <Field label={t('context.artifacts')} hint={t('submission.artifactFormat')} className="win-field--wide"><textarea rows={3} aria-invalid={!artifacts} value={draft.artifactRefs} onChange={(event) => setDraft((current) => ({ ...current, artifactRefs: event.target.value }))} /></Field>
      </div>
      {!rowsValid && <p className="win-inline-note win-inline-note--error" role="alert">{t('context.invalidRows')}</p>}
      {!sourceRunExists && <p className="win-inline-note win-inline-note--error" role="alert">{t('context.invalidSourceRun')}</p>}
      {tooLarge && <p className="win-inline-note win-inline-note--error" role="alert">{t('context.tooLarge')}</p>}
      <button type="button" className="win-button win-button--primary" disabled={busy || !rowsValid || !sourceRunExists || tooLarge} onClick={() => void onSave(packet)}>{busy ? t('common.loading') : t('context.save')}</button>
    </section>
  )
}

function ReviewDecision({
  task,
  submission,
  t,
  busy,
  onReview,
}: {
  task: Task
  submission: Submission | undefined
  t: (key: MessageKey) => string
  busy: boolean
  onReview: (decision: 'accept' | 'reject', reason: string) => Promise<void>
}) {
  const [reason, setReason] = useState('')
  useEffect(() => setReason(''), [task.submission_id])
  if (!task.submission_id) return <EmptyState>{t('task.noSubmission')}</EmptyState>
  const currentSubmission = submissionMatchesTask(submission, task) ? submission : undefined
  return (
    <section className="win-review-form">
      <div className="win-subheading-row"><h3>{t('review.title')}</h3><code className="win-micro-id">{task.submission_id}</code></div>
      {currentSubmission ? (
        <>
          <div className="win-task-meta"><span><b>{t('common.run')}</b>{currentSubmission.run_id}</span><span><b>{t('task.version')}</b>{currentSubmission.spec_version}</span></div>
          <div><h4>{t('submission.summary')}</h4><p className="win-inline-note">{currentSubmission.summary}</p></div>
          <div className="win-form-section"><h4>{t('submission.evidence')}</h4>
            {(currentSubmission.criterion_evidence ?? []).map((evidence) => {
              const criterion = task.spec.acceptance_criteria.find((item) => item.criterion_id === evidence.criterion_id)
              return <article className="win-evidence" key={evidence.criterion_id}>
                <strong>{criterion?.procedure || evidence.criterion_id}</strong>
                {criterion && <p className="win-evidence__expected">{criterion.expected}</p>}
                <p className="win-evidence__expected">{t('submission.revision')}: {evidence.revision}</p>
                <p className="win-evidence__expected">{t('submission.procedure')}: {evidence.procedure}</p>
                <p className="win-evidence__expected">{t('submission.result')}: {evidence.result}</p>
                <EvidenceResultPill passed={evidence.passed} t={t} />
              </article>
            })}
          </div>
          <div className="win-form-section"><h4>{t('submission.artifacts')}</h4>
            {currentSubmission.artifact_refs?.length ? currentSubmission.artifact_refs.map((artifact, index) => <div className="win-evidence" key={`${artifact.path_or_uri}-${index}`}><strong>{artifact.path_or_uri}</strong><p className="win-evidence__expected">{artifact.revision_or_hash}</p><p className="win-evidence__expected">{artifact.description}</p></div>) : <p className="win-inline-note">{t('common.none')}</p>}
          </div>
          <div className="win-form-section"><h4>{t('submission.limitations')}</h4><ul>{(currentSubmission.known_limitations ?? []).map((limitation, index) => <li key={`${index}-${limitation}`}>{limitation}</li>)}</ul></div>
        </>
      ) : <p className="win-inline-note win-inline-note--warning">{t('review.submissionUnavailable')}</p>}
      <Field label={t('review.reason')} hint={t('review.reasonHint')}><textarea rows={3} value={reason} onChange={(event) => setReason(event.target.value)} /></Field>
      <div className="win-actions">
        <button type="button" className="win-button win-button--primary" disabled={busy || task.state !== 'REVIEW' || !currentSubmission} onClick={() => void onReview('accept', reason.trim())}>{t('review.accept')}</button>
        <button type="button" className="win-button win-button--secondary" disabled={busy || task.state !== 'REVIEW' || !currentSubmission || !reason.trim()} onClick={() => void onReview('reject', reason.trim())}>{t('review.return')}</button>
      </div>
    </section>
  )
}

function SearchDrawer({
  projects,
  selectedProjectId,
  t,
}: {
  projects: Project[]
  selectedProjectId: string | null
  t: (key: MessageKey) => string
}) {
  const [query, setQuery] = useState('')
  const [projectId, setProjectId] = useState(selectedProjectId ?? '')
  const [results, setResults] = useState<unknown[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [searched, setSearched] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<MessageKey | null>(null)

  async function search(append: boolean) {
    if (!query.trim() || busy) return
    setBusy(true)
    setError(null)
    try {
      const page = await searchMessages(query.trim(), projectId || undefined, append ? cursor : null)
      setResults((current) => append ? [...current, ...page.items] : page.items)
      setCursor(page.next_cursor)
      setSearched(true)
    } catch (caught) {
      setError(keyForError(caught))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="win-drawer-content">
      <PanelHeader title={t('search.title')} />
      <form className="win-form-grid" onSubmit={(event) => { event.preventDefault(); void search(false) }}>
        <Field label={t('search.query')}><input value={query} onChange={(event) => setQuery(event.target.value)} autoFocus /></Field>
        <Field label={t('search.projectFilter')}>
          <select value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">{t('search.allProjects')}</option>{projects.filter((project) => !project.archived).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select>
        </Field>
        <button className="win-button win-button--primary" type="submit" disabled={busy || !query.trim()}>{busy ? t('common.loading') : t('search.submit')}</button>
      </form>
      {error && <p className="win-inline-note win-inline-note--error" role="alert">{t(error)}</p>}
      <div className="win-search-results" aria-live="polite">
        {results.map((item, index) => <article className="win-search-result" key={`${index}-${jsonText(item).slice(0, 40)}`}><p className="win-field__label">{t('search.rawResult')}</p><pre className="win-json">{jsonText(item)}</pre></article>)}
        {searched && results.length === 0 && <EmptyState>{t('search.empty')}</EmptyState>}
      </div>
      {cursor && <button type="button" className="win-button win-button--secondary" disabled={busy} onClick={() => void search(true)}>{t('search.more')}</button>}
    </div>
  )
}

function NotificationsDrawer({
  snapshot,
  tasks,
  t,
  busy,
  onRead,
  onOpenTask,
}: {
  snapshot: Snapshot
  tasks: Task[]
  t: (key: MessageKey) => string
  busy: boolean
  onRead: (notification: Notification) => Promise<void>
  onOpenTask: (taskId: string) => void
}) {
  const taskById = new Map(tasks.map((task) => [task.id, task]))
  const notifications = [...snapshot.notifications].sort((a, b) => b.created_at.localeCompare(a.created_at))
  return (
    <div className="win-drawer-content">
      <PanelHeader title={t('notification.title')} />
      <p className="win-inline-note">{t('notification.bodyUnavailable')}</p>
      <div className="win-notification-list">
        {notifications.map((notification) => {
          const task = taskById.get(notification.task_id)
          return (
            <article className={`win-notification ${notification.read ? '' : 'is-unread'}`} key={notification.id}>
              <div className="win-subheading-row"><strong>{notification.kind}</strong>{!notification.read && <span className="win-unread-tag">{t('notification.unread')}</span>}</div>
              <time className="win-list-item__detail" dateTime={notification.created_at}>{formatDate(notification.created_at)}</time>
              <code className="win-micro-id">{notification.id}</code>
              <div className="win-actions">
                {task && <button type="button" className="win-button win-button--quiet" onClick={() => onOpenTask(task.id)}>{task.spec.objective || task.id}</button>}
                {!notification.read && <button type="button" className="win-button win-button--secondary" disabled={busy} onClick={() => void onRead(notification)}>{t('notification.read')}</button>}
              </div>
            </article>
          )
        })}
        {notifications.length === 0 && <EmptyState>{t('notification.empty')}</EmptyState>}
      </div>
    </div>
  )
}

function DesktopSettingsDrawer({
  settings,
  available,
  busy,
  t,
  onChange,
}: {
  settings: { notifications: boolean; background: boolean }
  available: boolean
  busy: boolean
  t: (key: MessageKey) => string
  onChange: (next: { notifications: boolean; background: boolean }) => void
}) {
  return (
    <div className="win-drawer-content">
      <PanelHeader title={t('app.settings')} />
      {!available && <p className="win-inline-note">{t('desktopSettings.unavailable')}</p>}
      <label className="win-check">
        <input type="checkbox" checked={settings.notifications} disabled={!available || busy} onChange={(event) => onChange({ ...settings, notifications: event.target.checked })} />
        {t('desktopSettings.notifications')}
      </label>
      <p className="win-inline-note">{t('desktopSettings.notificationsHelp')}</p>
      <label className="win-check">
        <input type="checkbox" checked={settings.background} disabled={!available || busy} onChange={(event) => onChange({ ...settings, background: event.target.checked })} />
        {t('desktopSettings.background')}
      </label>
      <p className="win-inline-note">{t('desktopSettings.backgroundHelp')}</p>
    </div>
  )
}

function DiagnosticsDrawer({
  capabilities,
  connection,
  eventSequence,
  lastSync,
  t,
  onResync,
}: {
  capabilities: Capabilities | null
  connection: ConnectionState
  eventSequence: number
  lastSync: string
  t: (key: MessageKey) => string
  onResync: () => void
}) {
  const connectionLabel: Record<ConnectionState, MessageKey> = {
    connecting: 'app.connectionConnecting',
    live: 'app.connectionLive',
    disconnected: 'app.connectionDisconnected',
  }
  return (
    <div className="win-drawer-content">
      <PanelHeader title={t('diagnostics.title')} action={<button type="button" className="win-button win-button--secondary" onClick={onResync}>{t('app.resync')}</button>} />
      {capabilities ? (
        <dl className="win-diagnostics-list">
          <DiagnosticRow label={t('diagnostics.appVersion')} value={capabilities.app_version} />
          <DiagnosticRow label={t('diagnostics.schemaVersion')} value={String(capabilities.schema_version)} />
          <DiagnosticRow label={t('diagnostics.runtimeVersion')} value={capabilities.runtime_version || t('common.none')} />
          <DiagnosticRow label={t('diagnostics.runtimePath')} value={capabilities.runtime_path || t('common.none')} code />
          <DiagnosticRow label={t('diagnostics.chatEnabled')} value={t(capabilities.chat_enabled ? 'diagnostics.chatEnabledYes' : 'diagnostics.chatEnabledNo')} />
          {!capabilities.chat_enabled && <DiagnosticRow label={t('diagnostics.disabledReason')} value={capabilities.disabled_reason || t('error.commandUnsupported')} />}
          <DiagnosticRow label={t('diagnostics.connection')} value={t(connectionLabel[connection])} />
          <DiagnosticRow label={t('diagnostics.eventSequence')} value={String(eventSequence)} />
          <DiagnosticRow label={t('app.lastSync')} value={lastSync || t('common.none')} />
        </dl>
      ) : <EmptyState>{t('common.noData')}</EmptyState>}
      <p className="win-inline-note">{t('diagnostics.settingsUnavailable')}</p>
      <details className="win-font-license"><summary>M PLUS 1p Bold (SIL OFL 1.1)</summary><pre className="win-json">{dialogueFontLicense}</pre></details>
      <details className="win-font-license"><summary>Zen Kaku Gothic New Regular / Bold (SIL OFL 1.1)</summary><pre className="win-json">{cardFontLicense}</pre></details>
      <details className="win-font-license"><summary>Noto Sans JP Regular / Bold (SIL OFL 1.1)</summary><pre className="win-json">{headerFontLicense}</pre></details>
    </div>
  )
}

function DiagnosticRow({ label, value, code = false }: { label: string; value: string; code?: boolean }) {
  return <div className="win-diagnostic-row"><dt>{label}</dt><dd>{code ? <code>{value}</code> : value}</dd></div>
}

function WindowsApp() {
  const [locale, setLocale] = useState<Locale>('ja')
  const localeInitialized = useRef(false)
  useEffect(() => { document.documentElement.lang = locale === 'ja' ? 'ja-JP' : 'en-US' }, [locale])
  const t = useCallback((key: MessageKey) => translate(locale, key), [locale])
  const [desktopAvailable, setDesktopAvailable] = useState<boolean | null>(null)
  const [authorized, setAuthorized] = useState(false)
  const [capabilities, setCapabilities] = useState<Capabilities | null>(null)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null)
  const [detailTab, setDetailTab] = useState<DetailTab>('chat')
  const [messages, setMessages] = useState<Message[]>([])
  const [messageCursor, setMessageCursor] = useState<string | null>(null)
  const [messageReload, setMessageReload] = useState(0)
  const [drawer, setDrawer] = useState<Drawer>(null)
  const [pendingNotification, setPendingNotification] = useState<{ id: string; sequence: number } | null>(null)
  const activationSequence = useRef(0)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [agentEditTaskId, setAgentEditTaskId] = useState<string | null>(null)
  const [newTaskAgent, setNewTaskAgent] = useState<AgentProfile>(newAgentProfile)
  const [connection, setConnection] = useState<ConnectionState>('connecting')
  const [syncing, setSyncing] = useState(false)
  const [eventSequence, setEventSequence] = useState(0)
  const [lastSync, setLastSync] = useState('')
  const [errorKey, setErrorKey] = useState<MessageKey | null>(null)
  const [toastKey, setToastKey] = useState<MessageKey | null>(null)
  const [pendingAction, setPendingAction] = useState<string | null>(null)
  const [contractDraft, setContractDraft] = useState<ContractDraft>(() => newContractDraft())
  const [newTaskDraft, setNewTaskDraft] = useState<ContractDraft>(() => newContractDraft())
  const eventSequenceRef = useRef(0)

  useEffect(() => {
    setMessages([])
    setMessageCursor(null)
  }, [selectedTaskId])

  useEffect(() => {
    const bridge = window.chrome?.webview
    if (!bridge) {
      setDesktopAvailable(false)
      return
    }
    setDesktopAvailable(true)
    const onMessage = (event: WebViewMessageEvent) => {
      let value = event.data
      if (typeof value === 'string') {
        try { value = JSON.parse(value) as unknown } catch { return }
      }
      if (value && typeof value === 'object' && 'type' in value) {
        const response = value as { type?: unknown; token?: unknown; notification_id?: unknown }
        if (response.type === 'bootstrap' && typeof response.token === 'string' && response.token.length > 0) {
          setApiToken(response.token)
          setAuthorized(true)
        } else if (response.type === 'notification.activate' && typeof response.notification_id === 'string' && response.notification_id.length > 0 && response.notification_id.length <= 128) {
          const sequence = ++activationSequence.current
          setPendingNotification({ id: response.notification_id, sequence })
        }
      }
    }
    bridge.addEventListener('message', onMessage)
    bridge.postMessage(JSON.stringify({ type: 'bootstrap', version: 1 }))
    return () => {
      bridge.removeEventListener('message', onMessage)
      setApiToken(null)
    }
  }, [])

  const refreshSnapshot = useCallback(async () => {
    setSyncing(true)
    try {
      const next = await fetchSnapshot()
      if (!localeInitialized.current) { setLocale(next.settings?.locale === 'en-US' ? 'en' : 'ja'); localeInitialized.current = true }
      eventSequenceRef.current = next.event_seq
      setSnapshot(next)
      setEventSequence(next.event_seq)
      setLastSync(new Date().toLocaleTimeString())
      setErrorKey(null)
      setMessageReload((value) => value + 1)
    } catch (error) {
      setErrorKey(keyForError(error))
    } finally {
      setSyncing(false)
    }
  }, [])

  const refreshAll = useCallback(async () => {
    setSyncing(true)
    try {
      const [nextCapabilities, nextSnapshot] = await Promise.all([fetchCapabilities(), fetchSnapshot()])
      if (!localeInitialized.current) { setLocale(nextSnapshot.settings?.locale === 'en-US' ? 'en' : 'ja'); localeInitialized.current = true }
      setCapabilities(nextCapabilities)
      eventSequenceRef.current = nextSnapshot.event_seq
      setSnapshot(nextSnapshot)
      setEventSequence(nextSnapshot.event_seq)
      setLastSync(new Date().toLocaleTimeString())
      setErrorKey(null)
      setMessageReload((value) => value + 1)
    } catch (error) {
      setErrorKey(keyForError(error))
    } finally {
      setSyncing(false)
    }
  }, [])

  useEffect(() => {
    if (authorized) void refreshAll()
  }, [authorized, refreshAll])

  useEffect(() => {
    if (!authorized) return
    const controller = new AbortController()
    let active = true
    const delay = (milliseconds: number) => new Promise<void>((resolve) => {
      const timer = window.setTimeout(resolve, milliseconds)
      controller.signal.addEventListener('abort', () => { window.clearTimeout(timer); resolve() }, { once: true })
    })
    const reconnect = async () => {
      let wait = 900
      while (active && !controller.signal.aborted) {
        try {
          setConnection('connecting')
          await consumeEvents(eventSequenceRef.current, controller.signal, () => {
            if (active) setConnection('live')
          }, (sequence) => {
            if (!active) return
            if (sequence !== null) {
              eventSequenceRef.current = Math.max(eventSequenceRef.current, sequence)
              setEventSequence(eventSequenceRef.current)
            }
            void refreshSnapshot()
          })
          if (!active) return
          setConnection('disconnected')
        } catch {
          if (!active || controller.signal.aborted) return
          setConnection('disconnected')
        }
        await delay(wait)
        wait = Math.min(wait * 2, 8000)
      }
    }
    void reconnect()
    return () => {
      active = false
      controller.abort()
    }
  }, [authorized, refreshSnapshot])

  useEffect(() => {
    if (!authorized || !selectedTaskId) {
      setMessages([])
      setMessageCursor(null)
      return
    }
    let current = true
    void fetchMessages(selectedTaskId).then((page) => {
      if (!current) return
      setMessages(page.items)
      setMessageCursor(page.next_cursor)
    }).catch((error) => {
      if (current) setErrorKey(keyForError(error))
    })
    return () => { current = false }
  }, [authorized, selectedTaskId, messageReload])

  const projects = snapshot?.projects ?? []
  const roles = snapshot?.roles ?? []
  const tasks = snapshot?.tasks ?? []
  const submissions = snapshot?.submissions ?? []
  const selectedTask = tasks.find((task) => task.id === selectedTaskId)
  const selectedSubmission = selectedTask?.submission_id ? submissions.find((submission) => submission.submission_id === selectedTask.submission_id) : undefined
  const selectedProject = projects.find((project) => project.id === selectedTask?.spec.project_id)
  const selectedTaskRuns = snapshot?.runs.filter((run) => run.task_id === selectedTask?.id) ?? []
  const currentRun = selectedTask?.current_run_id ? snapshot?.runs.find((run) => run.id === selectedTask.current_run_id) : undefined
  const selectedContext = selectedTask ? (snapshot?.contexts ?? []).find((context) => context.task_id === selectedTask.id) : undefined
  const selectedTaskEffects = selectedTaskRuns.length
    ? (snapshot?.effects ?? []).filter((effect) => selectedTaskRuns.some((run) => run.id === effect.run_id))
    : []
  const selectedApprovals = (snapshot?.approvals ?? []).filter((approval) => selectedTaskRuns.some((run) => run.id === approval.run_id))
  const unreadCount = (snapshot?.notifications ?? []).filter((notification) => !notification.read).length
  useEffect(() => {
    if (!authorized || !pendingNotification) return
    let cancelled = false
    const sequence = pendingNotification.sequence
    void (async () => {
      try {
        const next = await fetchSnapshot()
        if (cancelled || activationSequence.current !== sequence) return
        if (!localeInitialized.current) {
          setLocale(next.settings?.locale === 'en-US' ? 'en' : 'ja')
          localeInitialized.current = true
        }
        eventSequenceRef.current = next.event_seq
        setSnapshot(next)
        setEventSequence(next.event_seq)
        setLastSync(new Date().toLocaleTimeString())
        setMessageReload((value) => value + 1)
        const notification = next.notifications.find((item) => item.id === pendingNotification.id)
        const task = notification && next.tasks.find((item) => item.id === notification.task_id)
        if (task) {
          setSelectedTaskId(task.id)
          setDetailTab('chat')
          setDrawer(null)
        }
        setPendingNotification((current) => current?.sequence === sequence ? null : current)
      } catch (error) {
        if (!cancelled && activationSequence.current === sequence) {
          setErrorKey(keyForError(error))
          setPendingNotification((current) => current?.sequence === sequence ? null : current)
        }
      }
    })()
    return () => { cancelled = true }
  }, [authorized, pendingNotification])
  const activeRun = currentRun ? ['queued', 'starting', 'running', 'waiting_approval', 'in_progress'].includes(currentRun.state.toLowerCase()) : false
  const canStartChat = !!selectedTask && !!capabilities?.chat_enabled && selectedTask.state === 'READY' && (!currentRun || (currentRun.workspace_quiet && ['succeeded', 'failed', 'cancelled', 'interrupted'].includes(currentRun.state)))
  const canCancel = !!selectedTask && !!currentRun && activeRun

  useEffect(() => {
    if (selectedTask) setContractDraft(contractDraftFromSpec(selectedTask.spec))
  }, [selectedTask?.id, selectedTask?.version])

  function openDialog(next: Dialog) {
    if (next === 'task') {
      setNewTaskAgent(newAgentProfile())
      const projectId = selectedProjectId ?? projects.find((project) => !project.archived)?.id ?? ''
      setNewTaskDraft(newContractDraft(projectId))
    }
    setErrorKey(null)
    setDialog(next)
  }

  async function command(action: string, options?: { task?: Task; data?: Record<string, unknown> }): Promise<boolean> {
    if (pendingAction) return false
    setPendingAction(action)
    setErrorKey(null)
    setToastKey(null)
    try {
      const task = options?.task
      await postCommand({
        request_id: makeId(),
        action,
        ...(task ? { task_id: task.id, expected_version: task.version } : {}),
        data: options?.data ?? {},
      })
      setToastKey('app.commandAccepted')
      await refreshSnapshot()
      return true
    } catch (error) {
      setErrorKey(keyForError(error))
      return false
    } finally {
      setPendingAction(null)
    }
  }

  async function createProject(name: string, path: string) {
    const created = await command('project.create', { data: { name, path } })
    if (created) setDialog(null)
  }

  async function changeLocale(next: Locale) {
    localeInitialized.current = true
    setLocale(next)
    await command('settings.locale', { data: { locale: next === 'ja' ? 'ja-JP' : 'en-US' } })
  }

  function changeDesktopSettings(next: { notifications: boolean; background: boolean }) {
    void command('settings.desktop', { data: next })
  }

  async function requestProjectTerminal(mode: 'open' | 'focus') {
    if (!selectedProject || selectedProject.archived || pendingAction) return
    setPendingAction(`terminal.${mode}`)
    setErrorKey(null)
    setToastKey(null)
    try {
      await openProjectTerminal({ project_id: selectedProject.id, mode })
      setToastKey('terminal.requested')
    } catch (error) {
      setErrorKey(keyForError(error))
    } finally {
      setPendingAction(null)
    }
  }

  async function createRole(name: string, prompt: string) {
    const created = await command('role.save', { data: { name, prompt } })
    if (created) setDialog(null)
  }

  async function createTask(event: FormEvent) {
    event.preventDefault()
    if (!newTaskDraft.projectId) { setErrorKey('error.projectRequired'); return }
    if (!newTaskDraft.objective.trim()) { setErrorKey('error.objectiveRequired'); return }
    if (!newTaskDraft.criteria.length || newTaskDraft.criteria.some((criterion) => !criterion.procedure.trim() || !criterion.expected.trim())) { setErrorKey('error.criteriaRequired'); return }
    if (!newTaskAgent.name.trim()) { setErrorKey('error.agentNameRequired'); return }
    const created = await command('task.create', { data: { spec: contractSpecFromDraft(newTaskDraft) as unknown as Record<string, unknown>, agent: { ...newTaskAgent, name: newTaskAgent.name.trim() } } })
    if (created) {
      setDialog(null)
      setSelectedProjectId(newTaskDraft.projectId)
    }
  }

  async function saveSpec() {
    if (!selectedTask) return
    if (!contractDraft.projectId) { setErrorKey('error.projectRequired'); return }
    if (!contractDraft.objective.trim()) { setErrorKey('error.objectiveRequired'); return }
    if (!contractDraft.criteria.length || contractDraft.criteria.some((criterion) => !criterion.procedure.trim() || !criterion.expected.trim())) { setErrorKey('error.criteriaRequired'); return }
    await command('task.revise', { task: selectedTask, data: { spec: contractSpecFromDraft(contractDraft, selectedTask.spec.version) } })
  }

  async function sendFirstMessage(text: string) {
    if (!selectedTask || !canStartChat) {
      setErrorKey('error.commandUnsupported')
      return
    }
    await command('task.start', { task: selectedTask, data: { input: text } })
  }

  async function submitWork(submission: SubmissionInput) {
    if (!selectedTask) return
    if (!currentRun || currentRun.task_id !== selectedTask.id || currentRun.spec_version !== selectedTask.spec.version) { setErrorKey('submission.runRequired'); return }
    await command('task.submit', { task: selectedTask, data: { submission } })
  }

  async function saveContext(task: Task, packet: ContextPacket) {
    await command('context.save', { task, data: { packet } })
  }

  async function respondApproval(approval: Approval, run: Run, task: Task, decision: 'accept' | 'decline') {
    await command('approval.respond', { task, data: { approval_id: approval.id, run_id: run.id, generation: approval.generation, decision } })
  }

  async function reviewSubmission(decision: 'accept' | 'reject', reason: string) {
    if (!selectedTask?.submission_id) return
    if (!submissionMatchesTask(selectedSubmission, selectedTask)) { setErrorKey('review.submissionUnavailable'); return }
    if (decision === 'reject' && !reason.trim()) { setErrorKey('review.reasonRequired'); return }
    await command('task.review', { task: selectedTask, data: { submission_id: selectedTask.submission_id, decision, reason } })
  }

  async function markNotificationRead(notification: Notification) {
    const task = tasks.find((item) => item.id === notification.task_id)
    await command('notification.read', { ...(task ? { task } : {}), data: { id: notification.id } })
  }

  async function loadMoreMessages() {
    if (!selectedTaskId || !messageCursor) return
    try {
      const page = await fetchMessages(selectedTaskId, messageCursor)
      setMessages((current) => [...page.items, ...current])
      setMessageCursor(page.next_cursor)
    } catch (error) {
      setErrorKey(keyForError(error))
    }
  }

  useEffect(() => {
    if (!drawer && !dialog && !agentEditTaskId) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { setDrawer(null); setDialog(null); setAgentEditTaskId(null) }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [drawer, dialog, agentEditTaskId])

  if (desktopAvailable === false) {
    return <BrowserNotice t={t} />
  }

  if (!authorized) {
    return <div className="win-bootstrap"><div className="win-bootstrap__mark">P</div><p>{t('app.bootstrapWaiting')}</p></div>
  }

  const connectionLabel: Record<ConnectionState, MessageKey> = {
    connecting: 'app.connectionConnecting',
    live: 'app.connectionLive',
    disconnected: 'app.connectionDisconnected',
  }

  return (
    <main className="win-app">
      <header className="win-topbar">
        <div className="win-brand"><span className="win-brand__mark" aria-hidden="true">P</span><div><strong>{t('app.title')}</strong><span>{t('app.tagline')}</span></div></div>
        <div className="win-topbar__tools">
          <span className={`win-connection win-connection--${connection}`} role="status"><i aria-hidden="true" />{t(connectionLabel[connection])}</span>
          <button type="button" className={`win-tool-button ${drawer === 'search' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'search' ? null : 'search')}>{t('app.search')}</button>
          <button type="button" className={`win-tool-button ${drawer === 'history' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'history' ? null : 'history')}>{t('app.history')}</button>
          <button type="button" className={`win-tool-button ${drawer === 'notifications' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'notifications' ? null : 'notifications')}>{t('app.notifications')}<span className="win-tool-count">{unreadCount}</span></button>
          <button type="button" className={`win-tool-button ${drawer === 'settings' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'settings' ? null : 'settings')}>{t('app.settings')}</button>
          <button type="button" className={`win-tool-button ${drawer === 'diagnostics' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'diagnostics' ? null : 'diagnostics')}>{t('app.diagnostics')}</button>
          <button type="button" className={`win-tool-button ${drawer === 'mcp' ? 'is-active' : ''}`} onClick={() => setDrawer(drawer === 'mcp' ? null : 'mcp')}>{t('app.mcp')}</button>
          <label className="win-language"><span className="sr-only">{t('app.language')}</span><select value={locale} onChange={(event) => { void changeLocale(event.target.value as Locale) }} aria-label={t('app.language')}><option value="ja">日本語</option><option value="en">English</option></select></label>
          <button type="button" className="win-icon-button" aria-label={t('app.resync')} title={t('app.resync')} onClick={() => void refreshAll()}>{syncing ? '…' : '↻'}</button>
        </div>
      </header>

      {capabilities?.app_version.startsWith('UI fixture') && <div className="win-preview-note" role="note">{capabilities.disabled_reason}</div>}
      <div className={`win-workspace win-workspace--desk ${selectedTask ? 'has-focus' : ''}`}>
        <TaskWorkspace
          snapshot={snapshot ?? emptySnapshot()}
          selectedProjectId={selectedProjectId}
          onProject={setSelectedProjectId}
          selectedTaskId={selectedTaskId}
          onEditAgent={setAgentEditTaskId}
          onSelect={(id) => { setSelectedTaskId(id); setDetailTab('chat') }}
          onCreate={() => openDialog('task')}
          onCreateProject={() => openDialog('project')}
          onCreateRole={() => openDialog('role')}
          onReorder={(order) => command('settings.board', { data: { order } })}
          busy={pendingAction !== null}
          locale={locale}
          originalAssets={capabilities?.local_preview_assets}
        />
        {selectedTask && <aside className="win-focus-pane" aria-label={selectedTask.spec.objective}>
          <div className="win-focus-heading">
            <div><p>{selectedProject?.name ?? t('nav.noProject')}</p><h1>{selectedTask.spec.objective}</h1></div>
            <button type="button" className="win-icon-button" aria-label={t('common.close')} onClick={() => setSelectedTaskId(null)}>×</button>
          </div>
          <div className="win-focus-meta"><StatePill value={selectedTask.state} t={t} />{currentRun && <StatePill value={currentRun.state} t={t} />}<span>{t('task.version')} {selectedTask.spec.version}</span></div>
          {desktopAvailable && capabilities?.terminal_available && selectedProject && !selectedProject.archived && <div className="win-actions">
            <button type="button" className="win-button win-button--secondary" disabled={pendingAction !== null} onClick={() => void requestProjectTerminal('open')}>{t('terminal.open')}</button>
            <button type="button" className="win-button win-button--secondary" disabled={pendingAction !== null} onClick={() => void requestProjectTerminal('focus')}>{t('terminal.focus')}</button>
          </div>}
          {desktopAvailable && capabilities?.terminal_available && selectedProject && !selectedProject.archived && <p className="win-inline-note">{t('terminal.help')}</p>}
          <nav className="win-detail-tabs" role="tablist" aria-label={t('detail.tabs')}>
            {(['chat', 'contract', 'context', 'review'] as const).map((tab, index, tabs) => <button
              key={tab} type="button" role="tab" id={`tab-${tab}`} aria-selected={detailTab === tab} aria-controls={`detail-${tab}`} tabIndex={detailTab === tab ? 0 : -1}
              onClick={() => setDetailTab(tab)} onKeyDown={(event) => {
                let next = index
                if (event.key === 'ArrowRight') next = (index + 1) % tabs.length
                else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length
                else if (event.key === 'Home') next = 0
                else if (event.key === 'End') next = tabs.length - 1
                else return
                event.preventDefault(); setDetailTab(tabs[next]); document.getElementById(`tab-${tabs[next]}`)?.focus()
              }}
            >{t(`detail.${tab}`)}</button>)}
          </nav>
        <section className="win-chat-pane win-detail-content" hidden={detailTab !== 'chat'} id="detail-chat" role="tabpanel" aria-labelledby="tab-chat">
          <div className="win-chat-heading"><div><h2>{t('chat.title')}</h2>{selectedTask && <span>{selectedTask.spec.objective || selectedTask.id}</span>}</div>{canCancel && <button type="button" className="win-button win-button--danger win-button--small" disabled={pendingAction !== null} onClick={() => selectedTask && void command('task.cancel', { task: selectedTask })}>{t('task.cancel')}</button>}</div>
          <div className="win-chat-scroll">
            {messageCursor && <button type="button" className="win-button win-button--quiet win-load-more" onClick={() => void loadMoreMessages()}>{t('common.refresh')}</button>}
            {selectedTask ? <MessageList messages={messages} t={t} /> : <EmptyState>{t('task.select')}</EmptyState>}
          </div>
          {selectedTask && (
            <div className="win-prompt-wrap">
              {!capabilities?.chat_enabled && <p className="win-prompt-note">{capabilities?.disabled_reason || t('task.chatUnavailable')}</p>}
              {capabilities?.chat_enabled && !canStartChat && <p className="win-prompt-note">{selectedTask.current_run_id ? t('chat.continuationUnavailable') : t('chat.needsReady')}</p>}
              <PromptInput
                sessionId={selectedTask.id}
                variant="chat"
                onSend={sendFirstMessage}
                placeholder={t('chat.prompt')}
                disabled={!canStartChat || pendingAction !== null}
                showSendButton
                sendLabel={t('chat.send')}
                sendShortcutDescription={t('chat.shortcut')}
                disableImageUpload
                enableSlashCommands={false}
              />
            </div>
          )}
        </section>

          <section className="win-contract-pane win-detail-content" hidden={detailTab !== 'contract'} id="detail-contract" role="tabpanel" aria-labelledby="tab-contract">
            <div className="win-pane-scroll">
                <PanelHeader title={t('task.specTitle')} />
                <ContractFields draft={contractDraft} setDraft={setContractDraft} projects={projects} roles={roles} t={t} />
                <div className="win-actions win-actions--wrap">
                  <button type="button" className="win-button win-button--secondary" disabled={pendingAction !== null || ['DONE', 'CANCELLED'].includes(selectedTask.state)} onClick={() => void saveSpec()}>{t('task.saveSpec')}</button>
                  <button type="button" className="win-button win-button--secondary" disabled={pendingAction !== null || selectedTask.state !== 'BACKLOG' || !contractMatchesTask(contractDraft, selectedTask.spec) || !selectedTask.spec.objective.trim()} onClick={() => void command('task.ready', { task: selectedTask })}>{t('task.confirmSpec')}</button>
                  {!contractMatchesTask(contractDraft, selectedTask.spec) && <p className="win-inline-note">{t('task.saveBeforeConfirm')}</p>}
                  <button type="button" className="win-button win-button--primary" disabled={pendingAction !== null || !canStartChat} onClick={() => { setDetailTab('chat'); window.requestAnimationFrame(() => document.querySelector<HTMLTextAreaElement>('.win-prompt-wrap textarea')?.focus()); setToastKey('chat.enterFirstMessage') }}>{t('task.start')}</button>
                </div>

              {selectedTask.state === 'DOING' && currentRun?.workspace_quiet && ['succeeded', 'failed', 'cancelled', 'interrupted'].includes(currentRun.state) && <RetryControl task={selectedTask} t={t} busy={pendingAction !== null} onRetry={(reason) => command('task.retry', { task: selectedTask, data: { reason } })} />}
              {canCancel && <button type="button" className="win-button win-button--danger" disabled={pendingAction !== null} onClick={() => void command('task.cancel', { task: selectedTask })}>{t('task.cancel')}</button>}
              {!capabilities?.chat_enabled && capabilities?.disabled_reason && <p className="win-inline-note win-inline-note--warning">{capabilities.disabled_reason}</p>}
            </div>
          </section>
          <section className="win-contract-pane win-detail-content" hidden={detailTab !== 'context'} id="detail-context" role="tabpanel" aria-labelledby="tab-context">
            <div className="win-pane-scroll">
              <TaskEvidenceStatus task={selectedTask} effects={selectedTaskEffects} submission={selectedSubmission} t={t} busy={pendingAction !== null} onResolveEffect={(effect, outcome, evidence) => command('recovery.resolve_effect', { task: selectedTask, data: { effect_id: effect.effect_id, outcome, evidence } })} onConfirmRecovery={(reason) => command('recovery.confirm', { task: selectedTask, data: { reason } })} />
              <ContextPacketForm task={selectedTask} runs={selectedTaskRuns} context={selectedContext} effects={selectedTaskEffects} t={t} busy={pendingAction !== null} onSave={(packet) => saveContext(selectedTask, packet)} />
              {selectedTask.block != null && selectedTask.block !== '' && <div className="win-block-note"><strong>{t('task.block')}</strong><pre>{jsonText(selectedTask.block)}</pre></div>}
            </div>
          </section>
          <section className="win-contract-pane win-detail-content" hidden={detailTab !== 'review'} id="detail-review" role="tabpanel" aria-labelledby="tab-review">
            <div className="win-pane-scroll win-review-section">
              <TaskEvidenceStatus task={selectedTask} effects={selectedTaskEffects} submission={selectedSubmission} t={t} busy={pendingAction !== null} onResolveEffect={(effect, outcome, evidence) => command('recovery.resolve_effect', { task: selectedTask, data: { effect_id: effect.effect_id, outcome, evidence } })} onConfirmRecovery={(reason) => command('recovery.confirm', { task: selectedTask, data: { reason } })} />
              <ReviewDecision task={selectedTask} submission={selectedSubmission} t={t} busy={pendingAction !== null} onReview={reviewSubmission} />
              <SubmissionForm task={selectedTask} run={currentRun} t={t} busy={pendingAction !== null} onSubmit={submitWork} />
            </div>
          </section>
        </aside>}
      </div>

      <footer className="win-statusbar"><span>{t('app.connection')}: {t(connectionLabel[connection])}</span><span>{syncing ? t('app.connectionSyncing') : t('app.lastSync')}: {syncing ? '' : lastSync || t('common.none')}</span><span>{t('diagnostics.eventSequence')}: {eventSequence}</span></footer>

      {(errorKey || toastKey) && <div className={`win-toast ${errorKey ? 'win-toast--error' : ''}`} role={errorKey ? 'alert' : 'status'}>{t(errorKey ?? toastKey!)}<button type="button" aria-label={t('common.close')} onClick={() => { setErrorKey(null); setToastKey(null) }}>×</button></div>}

      {drawer && snapshot && <DrawerShell label={t(drawer === 'search' ? 'search.title' : drawer === 'history' ? 'history.title' : drawer === 'notifications' ? 'notification.title' : drawer === 'mcp' ? 'mcp.title' : drawer === 'settings' ? 'app.settings' : 'diagnostics.title')} onClose={() => setDrawer(null)} t={t}>
        {drawer === 'search' && <SearchDrawer projects={projects} selectedProjectId={selectedProjectId} t={t} />}
        {drawer === 'history' && <HistoryDrawer projects={projects} tasks={tasks} selectedProjectId={selectedProjectId} selectedTaskId={selectedTaskId} t={t} onImported={async () => { await refreshAll() }} />}
        {drawer === 'notifications' && <NotificationsDrawer snapshot={snapshot} tasks={tasks} t={t} busy={pendingAction !== null} onRead={markNotificationRead} onOpenTask={(taskId) => { setSelectedTaskId(taskId); setDetailTab('chat'); setDrawer(null) }} />}
        {drawer === 'settings' && <DesktopSettingsDrawer settings={snapshot.settings?.desktop ?? { notifications: false, background: false }} available={desktopAvailable === true && capabilities?.desktop_enabled === true} busy={pendingAction !== null} t={t} onChange={changeDesktopSettings} />}
        {drawer === 'diagnostics' && <DiagnosticsDrawer capabilities={capabilities} connection={connection} eventSequence={eventSequence} lastSync={lastSync} t={t} onResync={() => void refreshAll()} />}
        {drawer === 'mcp' && <McpSettings t={t} />}
      </DrawerShell>}

      {dialog === 'project' && <ProjectDialog t={t} busy={pendingAction !== null} onClose={() => setDialog(null)} onSubmit={createProject} />}
      {dialog === 'role' && <RoleDialog t={t} busy={pendingAction !== null} onClose={() => setDialog(null)} onSubmit={createRole} />}
      {dialog === 'task' && <TaskDialog agent={newTaskAgent} setAgent={setNewTaskAgent} locale={locale} draft={newTaskDraft} setDraft={setNewTaskDraft} projects={projects} roles={roles} t={t} busy={pendingAction !== null} onClose={() => setDialog(null)} onSubmit={createTask} />}
      {agentEditTaskId && <Dialog title={locale === 'ja' ? '実行エージェントを設定' : 'Execution agent settings'} onClose={() => setAgentEditTaskId(null)} t={t}><AgentIdentityEditor key={agentEditTaskId} profile={snapshot?.settings?.agent_profiles?.[agentEditTaskId]} locale={locale} busy={pendingAction !== null} onSave={async (profile) => { const saved = await command('settings.agent', { data: { task_id: agentEditTaskId, ...profile } }); if (saved) setAgentEditTaskId(null); return saved }} /></Dialog>}
      {selectedTask && selectedApprovals.length > 0 && <ApprovalTray approvals={selectedApprovals} runs={selectedTaskRuns} task={selectedTask} t={t} busy={pendingAction !== null} onRespond={respondApproval} />}
    </main>
  )
}

function TaskDialog({
  agent,
  setAgent,
  locale,
  draft,
  setDraft,
  projects,
  roles,
  t,
  busy,
  onClose,
  onSubmit,
}: {
  agent: AgentProfile
  setAgent: (profile: AgentProfile) => void
  locale: Locale
  draft: ContractDraft
  setDraft: Dispatch<SetStateAction<ContractDraft>>
  projects: Project[]
  roles: Role[]
  t: (key: MessageKey) => string
  busy: boolean
  onClose: () => void
  onSubmit: (event: FormEvent) => Promise<void>
}) {
  return (
    <Dialog title={t('task.createSpec')} onClose={onClose} t={t}>
      <form onSubmit={(event) => void onSubmit(event)}>
        <AgentIdentityFields value={agent} onChange={setAgent} locale={locale} />
        <ContractFields draft={draft} setDraft={setDraft} projects={projects} roles={roles} t={t} />
        <p className="win-inline-note">{t('task.createHint')}</p>
        <div className="win-dialog-actions"><button type="button" className="win-button win-button--secondary" onClick={onClose}>{t('common.cancel')}</button><button type="submit" className="win-button win-button--primary" disabled={busy || !projects.length}>{t('task.create')}</button></div>
      </form>
    </Dialog>
  )
}

function RetryControl({
  task,
  t,
  busy,
  onRetry,
}: {
  task: Task
  t: (key: MessageKey) => string
  busy: boolean
  onRetry: (reason: string) => Promise<boolean>
}) {
  const [reason, setReason] = useState('')
  return (
    <form className="win-retry-control" onSubmit={(event) => { event.preventDefault(); if (reason.trim()) void onRetry(reason.trim()) }}>
      <label className="sr-only" htmlFor={`retry-${task.id}`}>{t('task.retryReason')}</label>
      <input id={`retry-${task.id}`} value={reason} onChange={(event) => setReason(event.target.value)} placeholder={t('task.retryReason')} />
      <button type="submit" className="win-button win-button--secondary" disabled={busy || !reason.trim()}>{t('task.retry')}</button>
    </form>
  )
}

function DrawerShell({ children, label, onClose, t }: { children: ReactNode; label: string; onClose: () => void; t: (key: MessageKey) => string }) {
  return (
    <div className="win-drawer-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose() }}>
      <aside className="win-drawer" role="dialog" aria-modal="true" aria-label={label}>
        <button type="button" className="win-drawer-close win-icon-button" aria-label={t('common.close')} onClick={onClose}>×</button>
        {children}
      </aside>
    </div>
  )
}

function ApprovalTray({
  approvals,
  runs,
  task,
  t,
  busy,
  onRespond,
}: {
  approvals: Approval[]
  runs: Run[]
  task: Task
  t: (key: MessageKey) => string
  busy: boolean
  onRespond: (approval: Approval, run: Run, task: Task, decision: 'accept' | 'decline') => Promise<void>
}) {
  const pending = approvals.filter((approval) => approval.state.toLowerCase() === 'pending')
  if (!pending.length) return null
  return (
    <aside className="win-approval-tray" aria-label={t('approval.title')}>
      <PanelHeader title={t('approval.title')} />
      <div className="win-approval-list">{pending.map((approval) => <ApprovalCard key={approval.id} approval={approval} run={runs.find((run) => run.id === approval.run_id)} task={task} t={t} busy={busy} onRespond={onRespond} />)}</div>
    </aside>
  )
}

function BrowserNotice({ t }: { t: (key: MessageKey) => string }) {
  return <main className="win-browser-notice"><div className="win-browser-notice__mark">P</div><h1>{t('app.openDesktopTitle')}</h1><p>{t('app.openDesktopBody')}</p></main>
}

function emptySnapshot(): Snapshot {
  return { projects: [], roles: [], tasks: [], runs: [], approvals: [], submissions: [], contexts: [], effects: [], notifications: [], event_seq: 0 }
}

export default WindowsApp
