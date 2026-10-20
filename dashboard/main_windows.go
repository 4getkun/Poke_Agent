//go:build windows && !legacy

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/history"
	"pokegents/dashboard/server/localapp"
	"pokegents/dashboard/server/maintenance"
	"pokegents/dashboard/server/mcpbridge"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	"pokegents/dashboard/server/updater"
	"pokegents/dashboard/web"
)

var appVersion = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "update" || os.Args[1] == "update-worker") {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := updater.RunCLI(ctx, os.Args[2:], appVersion, os.Args[1] == "update-worker"); err != nil {
			fmt.Fprintln(os.Stderr, "Pokegents update:", err)
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
		fmt.Fprintln(os.Stderr, "Pokegents:", err)
		headless := false
		for _, arg := range os.Args[1:] {
			name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if name == "verify" || name == "backup" || name == "restore" {
				headless = true
			}
		}
		if !headless {
			win.MessageBox(0, windows.StringToUTF16Ptr("起動または終了を完了できませんでした。診断情報を確認してください。\n"+err.Error()), windows.StringToUTF16Ptr("Pokegents"), win.MB_OK|win.MB_ICONERROR)
		}
		os.Exit(1)
	}
}
func run() error {
	defaultData, err := desktop.UserData()
	if err != nil {
		return err
	}
	codex := flag.String("codex", "", "Native Codex executable absolute path")
	codexHome := flag.String("codex-home", "", "Optional Codex history home (absolute path)")
	data := flag.String("data-dir", defaultData, "Application profile directory (local absolute path)")
	verify := flag.Bool("verify", false, "Verify database and embedded UI without opening a window")
	backup := flag.String("backup", "", "Back up a closed profile to a new absolute SQLite filename")
	restore := flag.String("restore", "", "Restore an existing SQLite backup into a closed profile")
	flag.Parse()
	if !filepath.IsAbs(*data) {
		return fmt.Errorf("保存先は絶対パスで指定してください")
	}
	operations := 0
	if *verify {
		operations++
	}
	if *backup != "" {
		operations++
	}
	if *restore != "" {
		operations++
	}
	if operations > 1 {
		return fmt.Errorf("verify / backup / restore は同時に指定できません")
	}
	var emptyMaintenance bool
	flag.Visit(func(f *flag.Flag) {
		if (f.Name == "backup" && *backup == "") || (f.Name == "restore" && *restore == "") {
			emptyMaintenance = true
		}
	})
	if emptyMaintenance {
		return fmt.Errorf("バックアップまたは復元するファイルの絶対パスを指定してください")
	}
	if *backup != "" || *restore != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if *backup != "" {
			if err := maintenance.Backup(ctx, filepath.Clean(*data), *backup); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"backup": "ok", "path": *backup})
		}
		previous, err := maintenance.Restore(ctx, filepath.Clean(*data), *restore)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"restore": "ok", "pre_restore_backup": previous})
	}
	instance, primary, err := desktop.LockProfile(filepath.Clean(*data))
	if err != nil {
		return err
	}
	if !primary {
		if *verify {
			return fmt.Errorf("PROFILE_IN_USE")
		}
		return nil
	}
	defer instance.Close()
	if err = desktop.PrepareProfile(*data); err != nil {
		return err
	}
	if err = maintenance.CheckPending(*data); err != nil {
		return err
	}
	if err = updater.CheckPending(*data); err != nil {
		return err
	}
	store, err := sqlstore.Open(filepath.Join(*data, "db", "pokegents.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	if *verify {
		if _, err = store.Snapshot(context.Background()); err != nil {
			return err
		}
		if _, err = fs.ReadFile(web.Assets(), "windows.html"); err != nil {
			return err
		}
		fmt.Println(`{"database":"ok","embedded_ui":"ok"}`)
		return nil
	}
	capabilities := localapp.Capabilities{AppVersion: appVersion, SchemaVersion: 1, RuntimePath: *codex, DesktopEnabled: true, TerminalAvailable: desktop.TerminalAvailable(), DisabledReason: "仕様書の残りの受け入れ確認が完了するまで、正式なエージェント実行は無効です"}
	if *codex != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		capabilities.RuntimeVersion, err = runtimeadapter.Probe(ctx, *codex, *data)
		cancel()
		if err != nil {
			capabilities.DisabledReason = "対応するネイティブ Codex 実行ファイルを選択してください"
		}
	}
	service := localapp.NewService(store, capabilities)
	service.History = history.New(store, history.Config{Executable: *codex, Home: *codexHome})
	if err = service.Recover(context.Background()); err != nil {
		return err
	}
	server, err := localapp.NewServer(service, web.Assets())
	if err != nil {
		return err
	}
	var host *desktop.Host
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := server.ConfigureMCP(*data, executable); err != nil {
		return err
	}
	desktopCtx, stopDesktop := context.WithCancel(context.Background())
	defer stopDesktop()
	var desktopOnce sync.Once
	var desktopWorkers sync.WaitGroup
	stopService := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return service.Close(ctx) == nil
	}
	host = desktop.New(desktop.Options{Origin: server.Origin, Token: server.Token, DataPath: filepath.Join(*data, "WebView2"), OnReady: func() {
		desktopOnce.Do(func() {
			desktopWorkers.Add(1)
			go func() { defer desktopWorkers.Done(); service.WatchDesktop(desktopCtx, host) }()
		})
	}, OnSessionEnd: stopService, CanClose: func() bool {
		if service.Active() {
			result := win.MessageBox(0, windows.StringToUTF16Ptr("実行中の作業を停止して終了しますか？"), windows.StringToUTF16Ptr("Pokegents"), win.MB_YESNO|win.MB_ICONQUESTION)
			if result != win.IDYES {
				return false
			}
		}
		return stopService()
	}})
	server.Native = host
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start() }()
	err = host.Run(instance)
	stopDesktop()
	desktopWorkers.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serviceErr := service.Close(ctx)
	serverErr := server.Close(ctx)
	if err != nil {
		return err
	}
	if serviceErr != nil {
		return serviceErr
	}
	return serverErr
}
