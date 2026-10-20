//go:build windows

package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	d "pokegents/dashboard/server/taskdomain"
)

type fakeDesktop struct {
	background, notifications bool
	unavailable               bool
	notices                   []string
	revision                  int64
}

func (f *fakeDesktop) ConfigureDesktop(background, notifications bool, revision int64) error {
	f.background, f.notifications = background, notifications
	f.revision = revision
	if f.unavailable {
		return errors.New("shell unavailable")
	}
	return nil
}

type fakeDesktopNative struct{ fakeDesktop }

func (f *fakeDesktopNative) PickDirectory() (string, error) { return "", nil }
func (f *fakeDesktopNative) OpenURL(string) error           { return nil }

func TestDesktopPreferencesApplyBeforeHTTPAcknowledgement(t *testing.T) {
	server := newHTTPTestServer(t)
	bridge := &fakeDesktopNative{}
	server.Native = bridge
	send := func(data string) *httptest.ResponseRecorder {
		return localRequest(server, http.MethodPost, "/api/commands", commandBody(t, Command{RequestID: d.ID(), Action: "settings.desktop", Data: json.RawMessage(data)}), true, server.Origin)
	}
	response := send(`{"notifications":false,"background":true}`)
	if response.Code != 200 || !bridge.background || bridge.notifications || bridge.revision == 0 {
		t.Fatalf("native preferences not applied before success: %d %s", response.Code, response.Body.String())
	}
	bridge.unavailable = true
	response = send(`{"notifications":true,"background":false}`)
	if response.Code == 200 || !strings.Contains(response.Body.String(), "DESKTOP_UNAVAILABLE") {
		t.Fatalf("native failure hidden: %d %s", response.Code, response.Body.String())
	}
	_, _, raw, err := server.Service.Store.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"notifications":true`) {
		t.Fatal("saved preference intent was lost")
	}
}
func (f *fakeDesktop) Notify(id, title, body string) error {
	f.notices = append(f.notices, id+"|"+title+"|"+body)
	return nil
}

func TestDesktopPreferencesAndNotificationDelivery(t *testing.T) {
	s := newHTTPTestServer(t).Service
	ctx := context.Background()
	command := func(action string, data string) error {
		_, err := s.Execute(ctx, Command{RequestID: d.ID(), Action: action, Data: json.RawMessage(data)})
		return err
	}
	if err := command("settings.locale", `{"locale":"en-US"}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"notifications":true}`, `{"notifications":null,"background":true}`, `{"notifications":"true","background":true}`} {
		if command("settings.desktop", bad) == nil {
			t.Fatalf("accepted invalid preferences %s", bad)
		}
	}
	update := func(id, kind string, read bool) {
		t.Helper()
		_, err := s.Store.Update(ctx, "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
			state.Projects["project"] = &d.Project{ID: "project", Name: "synthetic"}
			state.Tasks["task"] = &d.Task{ID: "task", State: "BACKLOG", Version: 1, Spec: d.Spec{ProjectID: "project", Objective: "対象タスク\n名前", Version: 1}}
			state.Notifications[id] = &d.Notification{ID: id, TaskID: "task", Kind: kind, Read: read, At: d.Now()}
			return true, nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	update("old", "approval", false)
	fake := &fakeDesktop{}
	watch := &desktopObservation{}
	watch.poll(ctx, s, fake)
	if fake.background || fake.notifications || len(fake.notices) != 0 {
		t.Fatal("defaults must be disabled")
	}
	if err := command("settings.desktop", `{"notifications":true,"background":true}`); err != nil {
		t.Fatal(err)
	}
	watch.poll(ctx, s, fake)
	if !fake.background || !fake.notifications || len(fake.notices) != 0 {
		t.Fatal("enabling replayed history")
	}
	update("new", "approval", false)
	update("read", "failed", true)
	update("ignored", "cancelled", false)
	watch.poll(ctx, s, fake)
	watch.poll(ctx, s, fake)
	if len(fake.notices) != 1 || fake.notices[0] != "new|Pokegents|Approval required\n対象タスク 名前" {
		t.Fatalf("wrong/doubled/private payload: %#v", fake.notices)
	}
	state, _, prefs, err := s.Store.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Notifications["new"].Read || !strings.Contains(string(prefs), `"locale":"en-US"`) {
		t.Fatal("delivery changed inbox or discarded preferences")
	}
	fake.unavailable = true
	update("offline", "interrupted", false)
	watch.poll(ctx, s, fake)
	fake.unavailable = false
	watch.poll(ctx, s, fake)
	if len(fake.notices) != 1 {
		t.Fatal("shell failure replayed old notifications")
	}
	if err := command("settings.desktop", `{"notifications":false,"background":false}`); err != nil {
		t.Fatal(err)
	}
	update("disabled", "failed", false)
	watch.poll(ctx, s, fake)
	if fake.background || fake.notifications || len(fake.notices) != 1 {
		t.Fatal("disable not respected")
	}
	if notificationLabel("approval", false) != "承認待ち" || notificationLabel("failed", false) != "エラー" || notificationLabel("succeeded", false) != "完了" {
		t.Fatal("wrong default labels")
	}
	if len([]rune(safeNotificationName(strings.Repeat("名", 200)))) != 80 {
		t.Fatal("unbounded target name")
	}
}
