//go:build windows

package localapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"testing/fstest"

	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

const spaMarker = "localapp test shell marker"

func newHTTPTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := sqlstore.Open(filepath.Join(t.TempDir(), "localapp.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assets := fstest.MapFS{
		"windows.html":        &fstest.MapFile{Data: []byte("<!doctype html>" + spaMarker)},
		"assets/app.js":       &fstest.MapFile{Data: []byte("test asset")},
		"assets/pkmn_r.ttf":   &fstest.MapFile{Data: []byte("font fixture data")},
		"assets/pkmn_r.woff2": &fstest.MapFile{Data: []byte("woff2 fixture data")},
	}
	var assetFS fs.FS = assets
	server, err := NewServer(NewService(store, Capabilities{
		AppVersion:  "test",
		ChatEnabled: false,
	}), assetFS)
	if err != nil {
		t.Fatalf("new localapp server: %v", err)
	}
	t.Cleanup(func() { _ = server.listener.Close() })
	return server
}

func TestHTTPFontAssetsUseFontMIMEAndNosniff(t *testing.T) {
	server := newHTTPTestServer(t)
	for _, test := range []struct {
		path, contentType, body string
	}{
		{"/assets/pkmn_r.ttf", "font/ttf", "font fixture data"},
		{"/assets/pkmn_r.woff2", "font/woff2", "woff2 fixture data"},
	} {
		response := localRequest(server, http.MethodGet, test.path, nil, false, "")
		if response.Code != http.StatusOK {
			t.Fatalf("font asset %s: status=%d body=%q", test.path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != test.contentType {
			t.Errorf("font asset %s Content-Type = %q, want %s", test.path, got, test.contentType)
		}
		if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("font asset %s X-Content-Type-Options = %q, want nosniff", test.path, got)
		}
		if got := response.Body.String(); got != test.body {
			t.Errorf("font asset %s body = %q, want exact font bytes", test.path, got)
		}
	}
}

func localRequest(server *Server, method, path string, body []byte, token bool, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, server.Origin+path, bytes.NewReader(body))
	req.Host = strings.TrimPrefix(server.Origin, "http://")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token {
		req.Header.Set("X-Pokegents-Token", server.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	return response
}

func commandBody(t *testing.T, command Command) []byte {
	t.Helper()
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return body
}

func jsonObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	return value
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	return fmt.Sprint(jsonObject(t, response)["code"])
}

func TestBoardOrderSettingsValidateMergeAndDeduplicate(t *testing.T) {
	server := newHTTPTestServer(t)
	ctx := context.Background()
	var taskID string
	_, err := server.Service.Store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects["board-project"] = &d.Project{ID: "board-project", Name: "Board", Identity: "board-workspace"}
		task, createErr := state.Create(d.Spec{
			ProjectID: "board-project", Objective: "Preview task",
			Criteria:     []d.Criterion{{ID: "criterion", Procedure: "check", Expected: "ok", Required: true}},
			AllowedScope: "fixture", OutOfScope: "external", ApprovalProfile: "read-only",
		})
		if createErr != nil {
			return nil, nil, createErr
		}
		taskID = task.ID
		return taskID, nil, nil
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	execute := func(id, action string, data string) error {
		_, commandErr := server.Service.Execute(ctx, Command{RequestID: id, Action: action, Data: json.RawMessage(data)})
		return commandErr
	}
	if err := execute("locale-en-request", "settings.locale", `{"locale":"en-US"}`); err != nil {
		t.Fatalf("set locale: %v", err)
	}
	orderData := fmt.Sprintf(`{"order":[%q]}`, taskID)
	if err := execute("board-order-request", "settings.board", orderData); err != nil {
		t.Fatalf("set board order: %v", err)
	}
	// Same request and body is idempotent and must not fail on a retry.
	if err := execute("board-order-request", "settings.board", orderData); err != nil {
		t.Fatalf("deduplicated board retry: %v", err)
	}
	if err := execute("locale-ja-request", "settings.locale", `{"locale":"ja-JP"}`); err != nil {
		t.Fatalf("restore locale: %v", err)
	}
	_, _, rawSettings, _, err := server.Service.Store.ViewBoard(ctx)
	if err != nil {
		t.Fatalf("ViewBoard settings: %v", err)
	}
	var prefs struct {
		Locale     string   `json:"locale"`
		BoardOrder []string `json:"board_order"`
	}
	if err := json.Unmarshal(rawSettings, &prefs); err != nil {
		t.Fatalf("decode preferences: %v", err)
	}
	if prefs.Locale != "ja-JP" || len(prefs.BoardOrder) != 1 || prefs.BoardOrder[0] != taskID {
		t.Fatalf("settings did not merge: locale=%q board_order=%v", prefs.Locale, prefs.BoardOrder)
	}
	if err := execute("board-duplicate-request", "settings.board", fmt.Sprintf(`{"order":[%q,%q]}`, taskID, taskID)); d.Code(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate task ID error=%v code=%s", err, d.Code(err))
	}
	if err := execute("board-unknown-request", "settings.board", `{"order":["missing-task"]}`); d.Code(err) != "INVALID_REQUEST" {
		t.Fatalf("unknown task ID error=%v code=%s", err, d.Code(err))
	}
}

func TestAgentProfileSettingsValidateMergeAndPersistOnReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agent-profiles.db")
	store, err := sqlstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	var taskID string
	_, err = store.Update(ctx, "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		state.Projects["agent-project"] = &d.Project{ID: "agent-project", Name: "Agent", Identity: "agent-workspace"}
		task, createErr := state.Create(d.Spec{
			ProjectID: "agent-project", Objective: "Agent profile task",
			Criteria:     []d.Criterion{{ID: "criterion", Procedure: "check", Expected: "ok", Required: true}},
			AllowedScope: "fixture", OutOfScope: "external", ApprovalProfile: "read-only",
		})
		if createErr != nil {
			return nil, nil, createErr
		}
		taskID = task.ID
		prefs, marshalErr := json.Marshal(map[string]any{"locale": "en-US", "board_order": []string{taskID}, "retained": "untouched"})
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		_, writeErr := tx.ExecContext(ctx, `INSERT INTO settings(id,payload) VALUES('preferences',?)`, string(prefs))
		return taskID, nil, writeErr
	})
	if err != nil {
		t.Fatalf("create task and preferences: %v", err)
	}
	service := NewService(store, Capabilities{})
	type agentProfileInput struct {
		TaskID string `json:"task_id"`
		Name   string `json:"name"`
		Sprite string `json:"sprite"`
	}
	requestNumber := 0
	execute := func(input agentProfileInput) error {
		requestNumber++
		data, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return marshalErr
		}
		_, commandErr := service.Execute(ctx, Command{RequestID: fmt.Sprintf("agent-profile-%04d", requestNumber), Action: "settings.agent", Data: data})
		return commandErr
	}
	if err := execute(agentProfileInput{TaskID: taskID, Name: "  図鑑係  ", Sprite: "slowking"}); err != nil {
		t.Fatalf("save agent profile: %v", err)
	}
	for _, input := range []agentProfileInput{
		{TaskID: taskID, Name: " \t ", Sprite: "pikachu"},
		{TaskID: taskID, Name: strings.Repeat("名", 81), Sprite: "pikachu"},
		{TaskID: taskID, Name: "図鑑係", Sprite: "missingno"},
		{TaskID: "missing-task", Name: "図鑑係", Sprite: "pikachu"},
	} {
		if err := execute(input); d.Code(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid profile error=%v code=%s input=%+v", err, d.Code(err), input)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before reopen: %v", err)
	}
	store = nil
	store, err = sqlstore.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	_, _, settings, err := store.View(ctx)
	if err != nil {
		t.Fatalf("view persisted settings: %v", err)
	}
	var persisted struct {
		Locale        string   `json:"locale"`
		BoardOrder    []string `json:"board_order"`
		Retained      string   `json:"retained"`
		AgentProfiles map[string]struct {
			Name   string `json:"name"`
			Sprite string `json:"sprite"`
		} `json:"agent_profiles"`
	}
	if err := json.Unmarshal(settings, &persisted); err != nil {
		t.Fatalf("decode persisted settings: %v", err)
	}
	profile, ok := persisted.AgentProfiles[taskID]
	if !ok || profile.Name != "図鑑係" || profile.Sprite != "slowking" {
		t.Fatalf("agent profile not persisted or not trimmed: %+v", persisted.AgentProfiles)
	}
	if persisted.Locale != "en-US" || len(persisted.BoardOrder) != 1 || persisted.BoardOrder[0] != taskID || persisted.Retained != "untouched" {
		t.Fatalf("agent profile update overwrote other settings: %+v", persisted)
	}
}

func TestTaskCreateAgentProfileIsAtomicAndOptional(t *testing.T) {
	ctx := context.Background()
	store, err := sqlstore.Open(filepath.Join(t.TempDir(), "task-create-profile.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	_, err = store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects["create-profile-project"] = &d.Project{ID: "create-profile-project", Name: "Create profile", Identity: "create-profile-workspace"}
		return true, nil, nil
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := NewService(store, Capabilities{})
	type createTaskInput struct {
		Spec  d.Spec             `json:"spec"`
		Agent *agentProfileInput `json:"agent,omitempty"`
	}
	makeSpec := func(objective string) d.Spec {
		return d.Spec{
			ProjectID: "create-profile-project", Objective: objective,
			Criteria:     []d.Criterion{{ID: "criterion", Procedure: "check", Expected: "ok", Required: true}},
			AllowedScope: "fixture", OutOfScope: "external", ApprovalProfile: "read-only",
		}
	}
	executeCreate := func(requestID string, input createTaskInput) error {
		data, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return marshalErr
		}
		_, commandErr := service.Execute(ctx, Command{RequestID: requestID, Action: "task.create", Data: data})
		return commandErr
	}
	if err := executeCreate("create-profile-valid", createTaskInput{Spec: makeSpec("名前付きの試験タスク"), Agent: &agentProfileInput{Name: "  調査係  ", Sprite: "hawlucha"}}); err != nil {
		t.Fatalf("create task with agent profile: %v", err)
	}
	state, _, settings, err := store.View(ctx)
	if err != nil {
		t.Fatalf("view created task: %v", err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("created task count=%d, want 1", len(state.Tasks))
	}
	var prefs struct {
		AgentProfiles map[string]struct {
			Name   string `json:"name"`
			Sprite string `json:"sprite"`
		} `json:"agent_profiles"`
	}
	if err := json.Unmarshal(settings, &prefs); err != nil {
		t.Fatalf("decode agent profile: %v", err)
	}
	var createdID string
	for id := range state.Tasks {
		createdID = id
	}
	profile := prefs.AgentProfiles[createdID]
	if profile.Name != "調査係" || profile.Sprite != "hawlucha" {
		t.Fatalf("created task profile=%+v", profile)
	}
	if err := executeCreate("create-profile-invalid", createTaskInput{Spec: makeSpec("rollback invalid agent task"), Agent: &agentProfileInput{Name: "不正なsprite", Sprite: "missingno"}}); d.Code(err) != "INVALID_REQUEST" {
		t.Fatalf("invalid profile create error=%v code=%s", err, d.Code(err))
	}
	state, _, settings, err = store.View(ctx)
	if err != nil {
		t.Fatalf("view after invalid create: %v", err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("invalid agent task was not rolled back: task count=%d", len(state.Tasks))
	}
	var afterPrefs struct {
		AgentProfiles map[string]json.RawMessage `json:"agent_profiles"`
	}
	if err := json.Unmarshal(settings, &afterPrefs); err != nil {
		t.Fatalf("decode settings after invalid create: %v", err)
	}
	if len(afterPrefs.AgentProfiles) != 1 {
		t.Fatalf("invalid agent profile changed settings: %+v", afterPrefs.AgentProfiles)
	}
	if err := executeCreate("create-profile-legacy", createTaskInput{Spec: makeSpec("既存形式の試験タスク")}); err != nil {
		t.Fatalf("create legacy task without agent profile: %v", err)
	}
	state, _, _, err = store.View(ctx)
	if err != nil || len(state.Tasks) != 2 {
		t.Fatalf("legacy task without agent profile: tasks=%d err=%v", len(state.Tasks), err)
	}
}

func TestHTTPHostOriginAndCapabilitiesTokenBoundary(t *testing.T) {
	server := newHTTPTestServer(t)
	response := localRequest(server, http.MethodGet, "/api/capabilities", nil, true, server.Origin)
	if response.Code != http.StatusOK {
		t.Fatalf("valid host/origin/token: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), server.Token) {
		t.Fatal("capabilities response contains the local token")
	}
	if strings.Contains(server.Origin, server.Token) {
		t.Fatal("local token appears in the server URL")
	}
	if response.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("wildcard CORS origin was returned")
	}

	withoutToken := localRequest(server, http.MethodGet, "/api/capabilities", nil, false, server.Origin)
	if withoutToken.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status=%d body=%s", withoutToken.Code, withoutToken.Body.String())
	}
	badHostReq := httptest.NewRequest(http.MethodGet, server.Origin+"/api/capabilities", nil)
	badHostReq.Host = "attacker.invalid"
	badHostReq.Header.Set("Origin", server.Origin)
	badHostReq.Header.Set("X-Pokegents-Token", server.Token)
	badHost := httptest.NewRecorder()
	server.ServeHTTP(badHost, badHostReq)
	if badHost.Code != http.StatusForbidden {
		t.Fatalf("bad Host: status=%d body=%s", badHost.Code, badHost.Body.String())
	}
	badOrigin := localRequest(server, http.MethodGet, "/api/capabilities", nil, true, "http://attacker.invalid")
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("different Origin: status=%d body=%s", badOrigin.Code, badOrigin.Body.String())
	}
}

func TestDesktopImagesRemainSameOrigin(t *testing.T) {
	server := newHTTPTestServer(t)
	response := localRequest(server, http.MethodGet, "/", nil, false, "")
	policy := response.Header().Get("Content-Security-Policy")
	var imageSources []string
	for _, directive := range strings.Split(policy, ";") {
		parts := strings.Fields(directive)
		if len(parts) > 0 && parts[0] == "img-src" {
			imageSources = parts[1:]
		}
	}
	if len(imageSources) != 1 || imageSources[0] != "'self'" {
		t.Fatalf("desktop images must stay on the authenticated app origin: %v", imageSources)
	}
	for _, forbidden := range []string{"script-src 'self' 'unsafe-inline'", "img-src *", "img-src data:", "img-src http:"} {
		if strings.Contains(policy, forbidden) {
			t.Fatalf("unexpected resource permission: %s", forbidden)
		}
	}
}

func TestHTTPUnknownAPIAndEventsRequireTokenWithoutSPAFallback(t *testing.T) {
	server := newHTTPTestServer(t)
	unknown := localRequest(server, http.MethodGet, "/api/not-a-route", nil, true, server.Origin)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown API: status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	if strings.Contains(unknown.Body.String(), spaMarker) || strings.Contains(unknown.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("unknown API returned SPA HTML: type=%q body=%q", unknown.Header().Get("Content-Type"), unknown.Body.String())
	}
	if unknown.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("unknown API returned wildcard CORS")
	}

	events := localRequest(server, http.MethodGet, "/api/events", nil, false, server.Origin)
	if events.Code != http.StatusUnauthorized {
		t.Fatalf("SSE without token: status=%d body=%s", events.Code, events.Body.String())
	}
}

func TestHTTPCommandsLifecycleDedupAndJSONValidation(t *testing.T) {
	server := newHTTPTestServer(t)
	workspace := t.TempDir()
	project := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, Command{
		RequestID: "project-create-001",
		Action:    "project.create",
		Data:      json.RawMessage(fmt.Sprintf(`{"name":"test","path":%q}`, workspace)),
	}), true, server.Origin)
	if project.Code != http.StatusOK {
		t.Fatalf("project.create: status=%d body=%s", project.Code, project.Body.String())
	}
	projectID := fmt.Sprint(jsonObject(t, project)["id"])
	if projectID == "" || projectID == "<nil>" {
		t.Fatalf("project.create response lacks project id: %s", project.Body.String())
	}

	spec := d.Spec{
		ProjectID:       projectID,
		Objective:       "test objective",
		Criteria:        []d.Criterion{{ID: "c1", Procedure: "run test", Expected: "pass", Required: true}},
		AllowedScope:    "test directory",
		OutOfScope:      "outside workspace",
		ApprovalProfile: "workspace-write",
	}
	taskCreate := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, Command{
		RequestID: "task-create-001",
		Action:    "task.create",
		Data:      json.RawMessage(mustMarshalForTest(t, map[string]any{"spec": spec})),
	}), true, server.Origin)
	if taskCreate.Code != http.StatusOK {
		t.Fatalf("task.create: status=%d body=%s", taskCreate.Code, taskCreate.Body.String())
	}
	taskValue := jsonObject(t, taskCreate)
	taskID := fmt.Sprint(taskValue["id"])
	version := int64(taskValue["version"].(float64))

	readyCommand := Command{RequestID: "task-ready-001", Action: "task.ready", TaskID: taskID, ExpectedVersion: version}
	readyBody := commandBody(t, readyCommand)
	ready := localRequest(server, http.MethodPost, "/api/commands", readyBody, true, server.Origin)
	if ready.Code != http.StatusOK {
		t.Fatalf("task.ready: status=%d body=%s", ready.Code, ready.Body.String())
	}
	readyResult := append([]byte(nil), ready.Body.Bytes()...)
	duplicate := localRequest(server, http.MethodPost, "/api/commands", readyBody, true, server.Origin)
	if duplicate.Code != http.StatusOK || !bytes.Equal(readyResult, duplicate.Body.Bytes()) {
		t.Fatalf("same request replay: status=%d body=%s, want status=200 result=%s", duplicate.Code, duplicate.Body.String(), readyResult)
	}
	changed := readyCommand
	changed.Data = json.RawMessage(`{}`)
	reused := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, changed), true, server.Origin)
	if reused.Code != http.StatusConflict || errorCode(t, reused) != "REQUEST_ID_REUSED" {
		t.Fatalf("changed body for request ID: status=%d body=%s", reused.Code, reused.Body.String())
	}

	stale := readyCommand
	stale.RequestID = "task-ready-stale-001"
	stale.ExpectedVersion = version
	staleResponse := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, stale), true, server.Origin)
	if staleResponse.Code != http.StatusConflict || errorCode(t, staleResponse) != "STALE_VERSION" {
		t.Fatalf("stale task version: status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}

	start := Command{RequestID: "task-start-gate-001", Action: "task.start", TaskID: taskID, ExpectedVersion: version + 1, Data: json.RawMessage(`{"input":"hello"}`)}
	startResponse := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, start), true, server.Origin)
	if startResponse.Code != http.StatusBadRequest || errorCode(t, startResponse) != "RUNTIME_GATE_CLOSED" {
		t.Fatalf("task.start with ChatEnabled=false: status=%d body=%s", startResponse.Code, startResponse.Body.String())
	}

	snapshot := localRequest(server, http.MethodGet, "/api/snapshot", nil, true, server.Origin)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot: status=%d body=%s", snapshot.Code, snapshot.Body.String())
	}
	var state struct {
		Projects []json.RawMessage `json:"projects"`
		Tasks    []struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Version int64  `json:"version"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(snapshot.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(state.Projects) != 1 || len(state.Tasks) != 1 || state.Tasks[0].ID != taskID || state.Tasks[0].State != "READY" || state.Tasks[0].Version != version+1 {
		t.Fatalf("snapshot arrays/state mismatch: projects=%d tasks=%+v", len(state.Projects), state.Tasks)
	}

	unknownField := []byte(`{"request_id":"json-check-002","action":"unknown","extra":true}`)
	trailingJSON := []byte(`{"request_id":"json-check-003","action":"unknown"} {}`)
	tooLarge := []byte(strings.Repeat(" ", (1<<20)+1))
	for name, body := range map[string][]byte{
		"unknown field":   unknownField,
		"trailing JSON":   trailingJSON,
		"body size limit": tooLarge,
	} {
		t.Run(name, func(t *testing.T) {
			response := localRequest(server, http.MethodPost, "/api/commands", body, true, server.Origin)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid JSON body: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPFiftyConcurrentReadyRequestsIncrementOnce(t *testing.T) {
	server := newHTTPTestServer(t)
	workspace := t.TempDir()
	project := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, Command{
		RequestID: "parallel-project-001",
		Action:    "project.create",
		Data:      json.RawMessage(fmt.Sprintf(`{"name":"parallel","path":%q}`, workspace)),
	}), true, server.Origin)
	if project.Code != http.StatusOK {
		t.Fatalf("project.create: status=%d body=%s", project.Code, project.Body.String())
	}
	projectID := fmt.Sprint(jsonObject(t, project)["id"])
	spec := d.Spec{ProjectID: projectID, Objective: "parallel objective", Criteria: []d.Criterion{{ID: "c1", Procedure: "run", Expected: "pass", Required: true}}, AllowedScope: "workspace", ApprovalProfile: "read-only"}
	taskCreate := localRequest(server, http.MethodPost, "/api/commands", commandBody(t, Command{
		RequestID: "parallel-task-001",
		Action:    "task.create",
		Data:      json.RawMessage(mustMarshalForTest(t, map[string]any{"spec": spec})),
	}), true, server.Origin)
	if taskCreate.Code != http.StatusOK {
		t.Fatalf("task.create: status=%d body=%s", taskCreate.Code, taskCreate.Body.String())
	}
	task := jsonObject(t, taskCreate)
	command := Command{RequestID: "parallel-ready-same-id", Action: "task.ready", TaskID: fmt.Sprint(task["id"]), ExpectedVersion: int64(task["version"].(float64))}
	body := commandBody(t, command)
	before := server.Service.Store.LastSeq(context.Background())

	const workers = 50
	responses := make([]*httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range responses {
		go func(index int) {
			defer wg.Done()
			responses[index] = localRequest(server, http.MethodPost, "/api/commands", body, true, server.Origin)
		}(i)
	}
	wg.Wait()

	var canonical []byte
	for i, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("parallel response %d: status=%d body=%s", i, response.Code, response.Body.String())
		}
		if i == 0 {
			canonical = append([]byte(nil), response.Body.Bytes()...)
		} else if !bytes.Equal(canonical, response.Body.Bytes()) {
			t.Fatalf("parallel response %d differs: %s != %s", i, response.Body.String(), canonical)
		}
	}
	if after := server.Service.Store.LastSeq(context.Background()); after != before+1 {
		t.Fatalf("same request ID emitted %d events; want exactly 1", after-before)
	}
	state, err := server.Service.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if got := state.Tasks[command.TaskID].Version; got != command.ExpectedVersion+1 {
		t.Fatalf("task version after 50 same requests=%d, want %d", got, command.ExpectedVersion+1)
	}
}

func mustMarshalForTest(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return body
}
