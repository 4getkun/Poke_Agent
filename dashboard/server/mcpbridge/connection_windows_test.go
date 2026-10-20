//go:build windows

package mcpbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConnectionRejectsNonLoopbackAndUnsafePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	c := Connection{Version: 1, Origin: "http://127.0.0.1:34567", Token: strings.Repeat("ab", 32), Scope: "read"}
	for _, origin := range []string{"http://localhost:1234", "http://127.0.0.1.evil:1234", "http://user@127.0.0.1:1234", "https://127.0.0.1:1234", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?token=x", "http://127.0.0.1:0"} {
		bad := c
		bad.Origin = origin
		if err := WriteConnection(path, bad); err == nil {
			t.Fatalf("accepted invalid origin %q", origin)
		}
	}
	if err := WriteConnection(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadConnection(path)
	if err != nil || loaded != c {
		t.Fatal("private connection round-trip failed", err)
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + u.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConnection(path); err == nil {
		t.Fatal("accepted a world-readable MCP credential")
	}
}

func TestHTTPGatewayReloadsConnectionAndDoesNotRedirect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	token := strings.Repeat("cd", 32)
	var firstCalls, secondCalls, redirectCalls atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectCalls.Add(1) }))
	defer redirect.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		if r.Header.Get("X-Pokegents-MCP-Token") != token || r.Header.Get("X-Pokegents-Token") != "" {
			t.Error("wrong dedicated authentication header")
		}
		if r.URL.Path == "/api/mcp/tools/redirect" {
			http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":1}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		if r.Header.Get("X-Pokegents-MCP-Token") != token {
			t.Error("missing MCP credential on reconnect")
		}
		_, _ = w.Write([]byte(`{"server":2}`))
	}))
	defer second.Close()
	c := Connection{Version: 1, Origin: first.URL, Token: token, Scope: "read"}
	if err := WriteConnection(path, c); err != nil {
		t.Fatal(err)
	}
	gateway := HTTPGateway(path)
	if _, err := gateway(context.Background(), "board_list", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway(context.Background(), "redirect", json.RawMessage(`{}`)); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirectCalls.Load() != 0 {
		t.Fatal("credential forwarded to redirect target")
	}
	c.Origin = second.URL
	if err := WriteConnection(path, c); err != nil {
		t.Fatal(err)
	}
	result, err := gateway(context.Background(), "board_list", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(result), `"server":2`) {
		t.Fatal("failed to reload changed app endpoint", err)
	}
	if firstCalls.Load() != 2 || secondCalls.Load() != 1 {
		t.Fatal("unexpected endpoint call counts")
	}
}
