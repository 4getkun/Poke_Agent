import { useState, type FormEvent } from 'react'
import type { AgentProfile } from './api'
import type { Locale } from './i18n'

export const newAgentProfile = (): AgentProfile => ({ name: '', sprite: 'slowking' })

export function AgentIdentityFields({ value, onChange, locale }: { value: AgentProfile; onChange: (value: AgentProfile) => void; locale: Locale }) {
  const ja = locale === 'ja'
  const sprites = [['slowking', 'ヤドキング'], ['hawlucha', 'ルチャブル'], ['bulbasaur', 'フシギダネ'], ['pikachu', 'ピカチュウ'], ['squirtle', 'ゼニガメ'], ['charmander', 'ヒトカゲ']]
  return <fieldset className="win-agent-identity"><legend>{ja ? '実行エージェント' : 'Execution agent'}</legend>
    <label className="win-field"><span className="win-field__label">{ja ? '付ける名前' : 'Name'}</span><input value={value.name} required maxLength={80} onChange={(event) => onChange({ ...value, name: event.target.value })} /></label>
    <label className="win-field"><span className="win-field__label">{ja ? 'ドット絵の割り当て' : 'Sprite assignment'}</span><select value={value.sprite} onChange={(event) => onChange({ ...value, sprite: event.target.value })}>{sprites.map(([id, label]) => <option key={id} value={id}>{ja ? label : id}</option>)}</select></label>
  </fieldset>
}

export function AgentIdentityEditor({ profile, locale, busy, onSave }: { profile?: AgentProfile; locale: Locale; busy: boolean; onSave: (profile: AgentProfile) => Promise<boolean> }) {
  const [draft, setDraft] = useState<AgentProfile>(() => profile ?? newAgentProfile())
  const submit = (event: FormEvent) => { event.preventDefault(); if (draft.name.trim()) void onSave({ ...draft, name: draft.name.trim() }) }
  return <form onSubmit={submit}><AgentIdentityFields value={draft} onChange={setDraft} locale={locale} /><div className="win-dialog-actions"><button type="submit" className="win-button win-button--primary" disabled={busy || !draft.name.trim()}>{locale === 'ja' ? '名前とドット絵を保存' : 'Save name and sprite'}</button></div></form>
}
