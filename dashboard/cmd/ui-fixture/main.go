//go:build windows && testfixture

// ui-fixture is an explicitly compiled, temporary UI verification harness.
// It never opens the user's app profile or enables runtime execution.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing/fstest"
	"time"

	"pokegents/dashboard/server/localapp"
	"pokegents/dashboard/server/mcpbridge"
	"pokegents/dashboard/server/processhost"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
	"pokegents/dashboard/web"
)

type fixtureAssets struct {
	base     fs.FS
	token    string
	localArt fs.FS
}

func (f *fixtureAssets) Open(name string) (fs.File, error) {
	if f.localArt != nil && (name == "town.png" || (strings.HasPrefix(name, "sprites/") && strings.HasSuffix(name, ".png"))) {
		return f.localArt.Open(name)
	}
	if name == "fixture-bridge.js" {
		b, _ := json.Marshal(f.token)
		script := `window.chrome={webview:{addEventListener:(_,fn)=>window.addEventListener('fixture-message',fn),removeEventListener:(_,fn)=>window.removeEventListener('fixture-message',fn),postMessage:()=>queueMicrotask(()=>window.dispatchEvent(new MessageEvent('fixture-message',{data:{type:'bootstrap',token:` + string(b) + `}})))}};`
		return fstest.MapFS{name: &fstest.MapFile{Data: []byte(script)}}.Open(name)
	}
	if name == "windows.html" {
		b, err := fs.ReadFile(f.base, name)
		if err != nil {
			return nil, err
		}
		body := strings.Replace(string(b), "</head>", `<script src="/fixture-bridge.js"></script></head>`, 1)
		return fstest.MapFS{name: &fstest.MapFile{Data: []byte(body)}}.Open(name)
	}
	return f.base.Open(name)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--emit-demo-owner" {
		owner, err := processhost.NewOwnership()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(owner); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := mcpbridge.RunCLI(context.Background(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "Pokegents MCP:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	demo := flag.Bool("demo", false, "seed synthetic task board data for UI verification")
	demoRecovery := flag.Bool("demo-recovery", false, "seed a synthetic recovery task (requires -demo)")
	localArt := flag.String("local-art", "", "local preview-only art directory; never included in the production build")
	webDir := flag.String("web-dir", "", "read built UI files directly during local UI verification")
	flag.Parse()
	if *demoRecovery && !*demo {
		return fmt.Errorf("-demo-recovery requires -demo")
	}
	root, err := os.MkdirTemp("", "pokegents-ui-fixture-")
	if err != nil {
		return err
	}
	defer func() {
		if filepath.Dir(root) == os.TempDir() && strings.HasPrefix(filepath.Base(root), "pokegents-ui-fixture-") {
			_ = os.RemoveAll(root)
		}
	}()
	store, err := sqlstore.Open(filepath.Join(root, "db", "app.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	disabledReason := "合成UI試験環境です。Codex実行は無効です。"
	if *demo {
		disabledReason = "合成データによるUI検証です。Codex実行は無効です。"
		if err := seedDemo(context.Background(), store, filepath.Join(root, "tempworkspace"), *demoRecovery); err != nil {
			return err
		}
	}
	service := localapp.NewService(store, localapp.Capabilities{AppVersion: "UI fixture (runtime disabled)", SchemaVersion: 1, DisabledReason: disabledReason, LocalPreviewAssets: *localArt != ""})
	if *demo {
		if err := seedAgentProfiles(context.Background(), store, service); err != nil {
			return err
		}
	}
	assets := &fixtureAssets{base: web.Assets()}
	if *webDir != "" {
		assets.base = os.DirFS(*webDir)
	}
	if *localArt != "" {
		assets.localArt = os.DirFS(*localArt)
	}
	server, err := localapp.NewServer(service, assets)
	if err != nil {
		return err
	}
	assets.token = server.Token
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := server.ConfigureMCP(root, executable); err != nil {
		return err
	}
	go server.Start()
	fmt.Printf("fixture_url=%s\nfixture_pid=%d\n", server.Origin, os.Getpid())
	<-time.After(60 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return server.Close(ctx)
}

type demoTask struct {
	title string
	state string
}

func seedDemo(ctx context.Context, store *sqlstore.Store, workspacePath string, recovery bool) error {
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return err
	}
	workspace, err := processhost.Workspace(workspacePath)
	if err != nil {
		return err
	}
	var recoveryWorkspace *d.Project
	if recovery {
		recoveryPath := filepath.Join(filepath.Dir(workspacePath), "recovery-workspace")
		if err := os.MkdirAll(recoveryPath, 0o755); err != nil {
			return err
		}
		recoveryWorkspace, err = processhost.Workspace(recoveryPath)
		if err != nil {
			return err
		}
	}
	tasks := []demoTask{
		{"READMEの手順を整える", "BACKLOG"},
		{"一覧画面の余白を確認する", "BACKLOG"},
		{"検索欄のラベルを見直す", "READY"},
		{"設定画面の説明を短くする", "READY"},
		{"会話パネルの読みやすさを調べる", "READY"},
		{"吹き出しとカードの対応を確認する", "BACKLOG"},
		{"通知の表示内容を点検する", "REVIEW"},
		{"起動時の案内文を仕上げる", "DONE"},
		{"試験用カードを整理する", "CANCELLED"},
		// Seed the live run last so completed demo runs release their workspace first.
		{"カードの並び順を確認する", "DOING"},
	}
	if recovery {
		tasks = append(tasks, demoTask{"中断した試験タスクを復旧する", "RECOVERY_REQUIRED"})
	}
	return seedDemoState(ctx, store, workspace, recoveryWorkspace, tasks)
}

func seedDemoState(ctx context.Context, store *sqlstore.Store, workspace, recoveryWorkspace *d.Project, tasks []demoTask) error {
	_, err := store.Update(ctx, "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		project := &d.Project{
			ID: workspace.ID, Name: "UI試験ワークスペース", DisplayPath: workspace.DisplayPath,
			ResolvedPath: workspace.ResolvedPath, Identity: workspace.Identity, Ancestors: workspace.Ancestors,
		}
		state.Projects[project.ID] = project
		if recoveryWorkspace != nil {
			recoveryProject := *recoveryWorkspace
			recoveryProject.Name = "UI復旧試験ワークスペース"
			state.Projects[recoveryProject.ID] = &recoveryProject
		}
		roles := []*d.Role{
			{ID: d.ID(), Name: "調査役", Prompt: "合成UI試験用の調査役です。"},
			{ID: d.ID(), Name: "実装役", Prompt: "合成UI試験用の実装役です。"},
		}
		for _, role := range roles {
			state.Roles[role.ID] = role
		}
		for i, item := range tasks {
			role := roles[i%len(roles)]
			taskProject := project
			if item.state == "RECOVERY_REQUIRED" {
				if recoveryWorkspace == nil {
					return nil, nil, fmt.Errorf("recovery task requires recovery workspace")
				}
				taskProject = state.Projects[recoveryWorkspace.ID]
			}
			task, createErr := state.Create(d.Spec{
				ProjectID: taskProject.ID, RoleID: role.ID, Objective: item.title,
				Criteria:     []d.Criterion{{ID: fmt.Sprintf("demo-%d", i+1), Procedure: "合成画面データを確認する", Expected: "画面に表示される", Required: true}},
				AllowedScope: "UI試験用の合成データ", OutOfScope: "実ユーザーデータと実行環境", ApprovalProfile: "read-only",
			})
			if createErr != nil {
				return nil, nil, createErr
			}
			var runID string
			switch item.state {
			case "BACKLOG":
			case "READY":
				if err := state.Ready(task); err != nil {
					return nil, nil, err
				}
			case "DOING", "REVIEW", "DONE":
				if err := state.Ready(task); err != nil {
					return nil, nil, err
				}
				run, beginErr := state.Begin(task, 4, "fixture", "fixture")
				if beginErr != nil {
					return nil, nil, beginErr
				}
				runID = run.ID
				if item.state == "DOING" {
					if err := state.Advance(task.ID, run.ID, run.Generation, "starting"); err != nil {
						return nil, nil, err
					}
					if err := state.Advance(task.ID, run.ID, run.Generation, "running"); err != nil {
						return nil, nil, err
					}
					break
				}
				for _, next := range []string{"starting", "running", "succeeded"} {
					if err := state.Advance(task.ID, run.ID, run.Generation, next); err != nil {
						return nil, nil, err
					}
				}
				if err := state.Quiesce(task.ID, run.ID, run.Generation); err != nil {
					return nil, nil, err
				}
				submission := d.Submission{
					TaskID: task.ID, RunID: run.ID, SpecVersion: task.Spec.Version,
					Summary:  "合成データによる確認結果です。",
					Evidence: []d.Evidence{{CriterionID: task.Spec.Criteria[0].ID, Revision: "fixture-1", Procedure: "画面の合成状態を確認", Result: "期待どおりに表示", Passed: true}},
				}
				if err := state.Submit(task, submission); err != nil {
					return nil, nil, err
				}
				if item.state == "DONE" {
					if err := state.Review(task, task.SubmissionID, "accept", ""); err != nil {
						return nil, nil, err
					}
				}
			case "CANCELLED":
				// The domain currently has no task-level cancel transition; this fixture
				// seeds only the terminal status and does not create a runtime/run.
				task.State = "CANCELLED"
				task.Version++
			case "RECOVERY_REQUIRED":
				if err := state.Ready(task); err != nil {
					return nil, nil, err
				}
				run, beginErr := state.Begin(task, 4, "fixture", "fixture")
				if beginErr != nil {
					return nil, nil, beginErr
				}
				runID = run.ID
				run.State, run.Error, run.EndedAt, run.WorkspaceQuiet = "interrupted", "HOST_RESTART", d.Now(), false
				task.Block = "RECOVERY_REQUIRED"
				task.Version++
				owner, err := emitReleasedDemoOwner()
				if err != nil {
					return nil, nil, err
				}
				ownerJSON, err := json.Marshal(owner)
				if err != nil {
					return nil, nil, err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_instances(id,run_id,generation,payload) VALUES(?,?,?,?)`, run.ID, run.ID, run.Generation, string(ownerJSON)); err != nil {
					return nil, nil, err
				}
				effect := &d.Effect{ID: d.ID(), RunID: run.ID, Description: "合成ファイル操作の結果を確認する", Outcome: "unknown"}
				state.Effects[effect.ID] = effect
			default:
				return nil, nil, fmt.Errorf("unsupported demo task state %q", item.state)
			}
			for messageIndex, body := range []string{"試験用の会話メッセージです。", "最新の合成出力をカードに表示します。"} {
				message := d.Message{ID: d.ID(), TaskID: task.ID, RunID: runID, Role: "assistant", Body: body, At: d.Now()}
				if messageIndex == 0 {
					message.Role = "user"
				}
				if err := sqlstore.AddMessage(ctx, tx, message); err != nil {
					return nil, nil, err
				}
			}
		}
		return true, nil, nil
	})
	return err
}

func emitReleasedDemoOwner() (processhost.Ownership, error) {
	executable, err := os.Executable()
	if err != nil {
		return processhost.Ownership{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--emit-demo-owner")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = processhost.Environment()
	output, err := command.Output()
	if err != nil {
		return processhost.Ownership{}, fmt.Errorf("emit synthetic ownership: %w", err)
	}
	var owner processhost.Ownership
	if err := json.Unmarshal(output, &owner); err != nil {
		return processhost.Ownership{}, err
	}
	if err := processhost.VerifyReleased(owner); err != nil {
		return processhost.Ownership{}, fmt.Errorf("verify released synthetic owner: %w", err)
	}
	return owner, nil
}

func seedAgentProfiles(ctx context.Context, store *sqlstore.Store, service *localapp.Service) error {
	state, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	tasks := make([]*d.Task, 0, len(state.Tasks))
	for _, task := range state.Tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Spec.Objective == tasks[j].Spec.Objective {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].Spec.Objective < tasks[j].Spec.Objective
	})
	prefixes := []string{"図鑑係", "調査係", "整備係"}
	sprites := []string{"slowking", "hawlucha", "bulbasaur", "pikachu", "squirtle", "charmander"}
	for i, task := range tasks {
		data, err := json.Marshal(struct {
			TaskID string `json:"task_id"`
			Name   string `json:"name"`
			Sprite string `json:"sprite"`
		}{TaskID: task.ID, Name: fmt.Sprintf("%s %02d", prefixes[i%len(prefixes)], i+1), Sprite: sprites[i%len(sprites)]})
		if err != nil {
			return err
		}
		if _, err := service.Execute(ctx, localapp.Command{RequestID: d.ID(), Action: "settings.agent", Data: data}); err != nil {
			return err
		}
	}
	return nil
}
