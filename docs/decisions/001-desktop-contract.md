# Windows用UIとサービス契約

初期実装では既存ACP/任意コマンドrouteを起動しない専用entryを追加する。
React/Vite、既存Markdown/PromptInput等を再利用し、プロバイダーwireはGo内で正規化する。
権利未確認のspriteと外部fontをWindowsビルドへ含めない。

## UI契約 v1

認証トークンはトップフレームのWebView2 message応答だけでメモリーに保持する。
通常ブラウザーで開いた場合は「デスクトップアプリから開いてください」を表示する。
テスト時のみ専用 Playwright init script でメモリーへ渡す。
`X-Pokegents-Token` を全API/fetch-streamに付け、URL/storageへ保存しない。

`GET /api/capabilities` は `{app_version,schema_version,runtime_version,runtime_path,chat_enabled,disabled_reason}`。
`GET /api/snapshot` は `{projects:[],roles:[],tasks:[],runs:[],approvals:[],notifications:[],event_seq}`。
`GET /api/messages?task_id=...&cursor=...` は `{items:[],next_cursor}`。
`GET /api/search?q=...&project_id=...&cursor=...` は同形式。
`GET /api/events?after=...` は認証fetch SSE。イベント受信後snapshotを再取得できる。

`POST /api/commands` は `{request_id,action,task_id?,expected_version?,data}`。
アクション: `project.create` (name,path)、`role.save` (id?,name,prompt)、
`task.create` (spec)、`task.ready`、`task.start` (input)、`task.cancel`、
`task.revise` (spec)、`task.submit` (submission)、`task.review` (submission_id,decision,reason)、
`approval.respond` (approval_id,run_id,generation,decision)、`task.retry`、
`recovery.confirm` (reason)、`notification.read` (id)。
変更は期待version・request IDを検査し、同一transactionで状態/監査/重複排除を記録する。
エラーは `{code,message_key,safe_details,retryable,correlation_id}`。

project: `{id,name,display_path,resolved_path,archived}`。
role: `{id,name,prompt}`。
task: `{id,spec,state,version,current_run_id,submission_id,block}`。
spec: `{project_id,role_id?,objective,acceptance_criteria:[{criterion_id,procedure,expected,required}],allowed_scope,out_of_scope,approval_profile,version}`。
approval_profileは `read-only` / `workspace-write`、モデルへの指示だけでは強制と表示しない。
run: `{id,task_id,spec_version,generation,state,provider_thread_id,provider_turn_id,started_at,ended_at,error}`。
approval: `{id,run_id,generation,kind,request,state,choices}`。choicesは一回限り `accept` / `decline` のみ。
message: `{id,task_id,run_id,role,body,created_at}`。
notification: `{id,task_id,kind,read,created_at}`。本文を含めない。
submission: `{submission_id,task_id,run_id,spec_version,summary,criterion_evidence:[{criterion_id,revision,procedure,result,passed}],artifact_refs:[],known_limitations:[]}`。
context: 仕様4.4の構造。UTF-8 16KiB超は拒否し、黙って切り詰めない。

UIが送るpathや状態は権限根拠にしない。全検査・外部起動はGo側で行う。
G1/G2未通過時はchat_enabled=falseとし、UIから開始できない。mock合格を実Codex対応としない。
