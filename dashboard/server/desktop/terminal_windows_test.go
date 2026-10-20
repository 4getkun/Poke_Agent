//go:build windows

package desktop

import (
	"reflect"
	"strings"
	"testing"
)

func TestTerminalArgumentsAreBoundedAndStable(t *testing.T) {
	path := `C:\日本語 workspace & () [x]`
	args, err := terminalArguments(`C:\User Data\WebView2`, path, "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 8 || args[0] != "--window" || !strings.HasPrefix(args[1], "Pokegents-") || args[2] != "new-tab" || args[4] != path || args[6] != "Pokegents" {
		t.Fatalf("unexpected arguments: %#v", args)
	}
	focus, err := terminalArguments(`c:\user data\webview2`, strings.ToLower(path), "focus")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(focus, []string{"--window", args[1], "focus-tab", "--target", "0"}) {
		t.Fatalf("window identity/focus changed: %#v", focus)
	}
	other, _ := terminalArguments(`C:\Other Profile`, path, "open")
	if other[1] == args[1] {
		t.Fatal("different profiles share terminal identity")
	}
	for _, bad := range []string{`relative`, `\\server\share`, `C:\a;new-tab`, "C:\\a\nnew-tab", `C:\a"b`} {
		if _, err := terminalArguments("profile", bad, "open"); err == nil {
			t.Fatalf("accepted unsafe argument %q", bad)
		}
	}
	if _, err := terminalArguments("profile", path, "command"); err == nil {
		t.Fatal("accepted arbitrary command mode")
	}
}
