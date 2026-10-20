package taskdomain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContextPacketValidateAcceptsExact16KiBUTF8Payload(t *testing.T) {
	packet := ContextPacket{SchemaVersion: 1, TaskID: "task-1", SpecVersion: 1}
	packet.Facts = []Fact{{Source: "test", Text: ""}}
	base, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	remaining := 16*1024 - len(base)
	if remaining < 0 {
		t.Fatal("test fixture exceeds the context limit before adding text")
	}
	packet.Facts[0].Text = strings.Repeat("あ", remaining/len("あ")) + strings.Repeat("x", remaining%len("あ"))

	atLimit, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(atLimit); got != 16*1024 {
		t.Fatalf("fixture is %d bytes; want exactly %d", got, 16*1024)
	}
	if err := packet.Validate(); err != nil {
		t.Fatalf("exactly 16 KiB UTF-8 context rejected: %v (%s)", err, Code(err))
	}

	packet.Facts[0].Text += "x"
	if err := packet.Validate(); Code(err) != "CONTEXT_TOO_LARGE" {
		t.Fatalf("one byte over limit: got %v (%s), want CONTEXT_TOO_LARGE", err, Code(err))
	}
}
