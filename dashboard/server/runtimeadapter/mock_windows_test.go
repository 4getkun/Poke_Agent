//go:build windows

package runtimeadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pokegents/dashboard/server/processhost"
)

const (
	mockModeEnv    = "POKEGENTS_RUNTIMEADAPTER_MOCK_MODE"
	mockTraceEnv   = "POKEGENTS_RUNTIMEADAPTER_MOCK_TRACE"
	mockFixtureEnv = "POKEGENTS_RUNTIMEADAPTER_MOCK_FIXTURES"
	mockTestName   = "TestRuntimeAdapterMockProcess"
)

type mockFrame struct {
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	JSONRPC json.RawMessage `json:"jsonrpc,omitempty"`
}

type mockTrace struct {
	Kind    string          `json:"kind"`
	Method  string          `json:"method,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	JSONRPC json.RawMessage `json:"jsonrpc,omitempty"`
}

type mockHarness struct {
	client   *Client
	process  *processhost.Process
	trace    string
	fixtures string
}

// TestRuntimeAdapterMockProcess is also the stdio fixture subprocess. The
// environment switch is passed only to this child by processhost.Start.
func TestRuntimeAdapterMockProcess(t *testing.T) {
	mode := os.Getenv(mockModeEnv)
	if mode == "" {
		return
	}
	if err := runMockProcess(mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func startMockClient(t *testing.T, mode string) *mockHarness {
	t.Helper()
	if SupportedVersion != "0.163.0" {
		t.Fatalf("mock fixtures pin Codex %s, runtimeadapter supports %s", "0.163.0", SupportedVersion)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := filepath.Abs(filepath.Join("testdata", "app-server-0.163.0"))
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "mock-trace.jsonl")
	env := append(processhost.Environment(),
		mockModeEnv+"="+mode,
		mockTraceEnv+"="+trace,
		mockFixtureEnv+"="+fixtures,
	)
	p, err := processhost.Start(exe, []string{"-test.run=^" + mockTestName + "$"}, cwd, env)
	if err != nil {
		t.Fatalf("start mock subprocess: %v", err)
	}
	c := &Client{process: p, pending: map[string]chan Frame{}, events: make(chan Event, 1024), done: make(chan struct{})}
	c.readers.Add(2)
	go c.read()
	go func() {
		defer c.readers.Done()
		_, err := io.Copy(io.Discard, p.Stderr)
		if err != nil {
			select {
			case <-c.done:
			default:
				c.fail(errors.New("runtime stderr pipe failure"))
			}
		}
	}()
	h := &mockHarness{client: c, process: p, trace: trace, fixtures: fixtures}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Errorf("close mock subprocess: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result json.RawMessage
	err = c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "pokegents_windows", "title": "Pokegents for Windows", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": false},
	}, &result)
	if err == nil {
		err = c.Notify("initialized", map[string]any{})
	}
	if err != nil {
		t.Fatalf("mock initialize handshake: %v", err)
	}
	return h
}

func runMockProcess(mode string) error {
	tracePath := os.Getenv(mockTraceEnv)
	fixtureDir := os.Getenv(mockFixtureEnv)
	if tracePath == "" || fixtureDir == "" {
		return errors.New("mock subprocess paths missing")
	}
	reader := bufio.NewReader(os.Stdin)
	initialized := false
	waitingUnknownResponse := false
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) == 0 {
			continue
		}
		var frame mockFrame
		if err := json.Unmarshal(bytes.TrimSpace(line), &frame); err != nil {
			return fmt.Errorf("decode client frame: %w", err)
		}
		if len(frame.JSONRPC) != 0 {
			return errors.New("client frame unexpectedly contains jsonrpc")
		}
		if err := writeMockTrace(tracePath, mockTrace{Kind: "client", Method: frame.Method, ID: frame.ID, Params: frame.Params, Result: frame.Result, Error: frame.Error, JSONRPC: frame.JSONRPC}); err != nil {
			return err
		}

		if frame.Method == "" {
			if !waitingUnknownResponse {
				continue
			}
			var response struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(line, &response); err != nil {
				return err
			}
			if string(response.ID) != "9001" || response.Error.Code != -32601 {
				return fmt.Errorf("unknown server request was not rejected: id=%s code=%d", response.ID, response.Error.Code)
			}
			return nil
		}
		if frame.Method != "initialized" {
			if _, err := strconv.ParseUint(string(frame.ID), 10, 64); err != nil {
				return fmt.Errorf("client request %q id is not numeric: %s", frame.Method, frame.ID)
			}
		} else if len(frame.ID) != 0 {
			return errors.New("initialized notification unexpectedly has an id")
		}

		switch frame.Method {
		case "initialize":
			result, err := readMockFixture(fixtureDir, "initialize-result.json")
			if err != nil {
				return err
			}
			if err := writeMockTrace(tracePath, mockTrace{Kind: "server", Method: frame.Method, ID: frame.ID, Result: result}); err != nil {
				return err
			}
			if err := writeMockFrame(mockFrame{ID: frame.ID, Result: result}); err != nil {
				return err
			}
			initialized = true
		case "initialized":
			if !initialized {
				return errors.New("initialized arrived before initialize response")
			}
			switch mode {
			case "unknown":
				b, err := readMockFixture(fixtureDir, "unknown-server-request.json")
				if err != nil {
					return err
				}
				if err := writeMockLine(b); err != nil {
					return err
				}
				waitingUnknownResponse = true
			case "oversize":
				return writeOversizeFrame()
			case "queue":
				return writeQueueOverflowFrames()
			case "stderr":
				if _, err := io.Copy(os.Stderr, strings.NewReader(strings.Repeat("x", 1<<20))); err != nil {
					return err
				}
			}
		case "thread/start", "thread/resume":
			result, err := readMockFixture(fixtureDir, "thread-result.json")
			if err != nil {
				return err
			}
			if mode == "unsafe-policy" && frame.Method == "thread/start" {
				var response map[string]json.RawMessage
				if err := json.Unmarshal(result, &response); err != nil {
					return err
				}
				response["sandbox"] = json.RawMessage(`{"type":"dangerFullAccess"}`)
				result, err = json.Marshal(response)
				if err != nil {
					return err
				}
			}
			if err := writeMockTrace(tracePath, mockTrace{Kind: "server", Method: frame.Method, ID: frame.ID, Result: result}); err != nil {
				return err
			}
			if err := writeMockFrame(mockFrame{ID: frame.ID, Result: result}); err != nil {
				return err
			}
		case "turn/start":
			result, err := readMockFixture(fixtureDir, "turn-result.json")
			if err != nil {
				return err
			}
			if err := writeMockTrace(tracePath, mockTrace{Kind: "server", Method: frame.Method, ID: frame.ID, Result: result}); err != nil {
				return err
			}
			if err := writeMockFrame(mockFrame{ID: frame.ID, Result: result}); err != nil {
				return err
			}
			if mode == "flow" {
				if err := writeFlowEvents(fixtureDir); err != nil {
					return err
				}
			}
		case "turn/interrupt":
			result, err := readMockFixture(fixtureDir, "interrupt-result.json")
			if err != nil {
				return err
			}
			if err := writeMockFrame(mockFrame{ID: frame.ID, Result: result}); err != nil {
				return err
			}
		default:
			if err := writeMockFrame(mockFrame{ID: frame.ID, Error: json.RawMessage(`{"code":-32601,"message":"Method not found"}`)}); err != nil {
				return err
			}
		}
	}
}

func writeFlowEvents(fixtureDir string) error {
	file, err := os.Open(filepath.Join(fixtureDir, "stream-events.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.Clone(scanner.Bytes())
		if err := writeMockLine(line); err != nil {
			return err
		}
		if lineNumber == 2 {
			large := mockFrame{Method: "item/agentMessage/delta", Params: mustJSON(map[string]any{
				"threadId": "thread-fixture", "turnId": "turn-fixture", "delta": strings.Repeat("あ", 24*1024),
			})}
			b, err := json.Marshal(large)
			if err != nil {
				return err
			}
			if len(b) <= 64<<10 {
				return fmt.Errorf("large fixture frame is only %d bytes", len(b))
			}
			if err := writeMockLine(b); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func writeOversizeFrame() error {
	b, err := json.Marshal(mockFrame{Method: "item/agentMessage/delta", Params: mustJSON(map[string]any{
		"threadId": "thread-fixture", "turnId": "turn-fixture", "delta": strings.Repeat("x", MaxFrame+1024),
	})})
	if err != nil {
		return err
	}
	return writeMockLine(b)
}

func writeQueueOverflowFrames() error {
	delta := strings.Repeat("あ", 24*1024)
	for i := 0; i < 120; i++ {
		b, err := json.Marshal(mockFrame{Method: "item/agentMessage/delta", Params: mustJSON(map[string]any{
			"threadId": "thread-fixture", "turnId": "turn-fixture", "delta": delta,
		})})
		if err != nil {
			return err
		}
		if err := writeMockLine(b); err != nil {
			return err
		}
	}
	return nil
}

func readMockFixture(dir, name string) (json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("invalid JSON fixture %s", name)
	}
	return json.RawMessage(b), nil
}

func writeMockTrace(path string, trace mockTrace) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(trace)
}

func writeMockFrame(frame mockFrame) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return writeMockLine(b)
}

func writeMockLine(b []byte) error {
	if len(b) == 0 || b[len(b)-1] != '\n' {
		b = append(bytes.Clone(b), '\n')
	}
	for len(b) != 0 {
		n, err := os.Stdout.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func readMockTrace(t *testing.T, path string) []mockTrace {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open mock trace: %v", err)
	}
	defer f.Close()
	var out []mockTrace
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry mockTrace
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode mock trace: %v", err)
		}
		out = append(out, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertNumericCalls(t *testing.T, trace []mockTrace, wantMethods []string) {
	t.Helper()
	var got []string
	for _, entry := range trace {
		if entry.Kind != "client" || entry.Method == "" {
			continue
		}
		got = append(got, entry.Method)
		if len(entry.JSONRPC) != 0 {
			t.Errorf("client method %q unexpectedly includes jsonrpc", entry.Method)
		}
		if entry.Method == "initialized" {
			if len(entry.ID) != 0 {
				t.Errorf("initialized notification has id %s", entry.ID)
			}
			continue
		}
		if id, err := strconv.ParseUint(string(entry.ID), 10, 64); err != nil || id == 0 {
			t.Errorf("client method %q has nonnumeric id %s", entry.Method, entry.ID)
		}
	}
	if !reflect.DeepEqual(got, wantMethods) {
		t.Errorf("client method order=%v, want %v", got, wantMethods)
	}
}

func waitForClientFailure(t *testing.T, c *Client) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	select {
	case <-c.done:
	case <-ctx.Done():
		t.Fatalf("client did not report failure before timeout: %v", ctx.Err())
	}
	for {
		_, err := c.Next(ctx)
		if err != nil {
			return err
		}
	}
}

func TestMockProtocolInitializeThreadTurnAndResume(t *testing.T) {
	h := startMockClient(t, "flow")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := h.client.Open(ctx, cwd, "read-only", "", "")
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if threadID != "thread-fixture" {
		t.Fatalf("thread id=%q", threadID)
	}
	turnID, err := h.client.Turn(ctx, threadID, "日本語で応答してください")
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	if turnID != "turn-fixture" {
		t.Fatalf("turn id=%q", turnID)
	}

	var output strings.Builder
	completed := false
	for !completed {
		frame, err := h.client.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(frame.Params, &params); err != nil {
			t.Fatalf("decode event %q: %v", frame.Method, err)
		}
		switch frame.Method {
		case "item/agentMessage/delta":
			if params.ThreadID == threadID && params.TurnID == turnID {
				output.WriteString(params.Delta)
			}
		case "turn/completed":
			if params.ThreadID != threadID || params.Turn.ID != turnID || params.Turn.Status != "completed" {
				t.Fatalf("completion mismatch: %#v", params)
			}
			completed = true
		default:
			t.Fatalf("unexpected event method %q", frame.Method)
		}
	}
	largeDelta := strings.Repeat("あ", 24*1024)
	if got, want := output.String(), "日本語の応答："+largeDelta; got != want {
		t.Fatalf("streamed output mismatch: bytes=%d want=%d", len(got), len(want))
	}
	largeFrame, err := json.Marshal(mockFrame{Method: "item/agentMessage/delta", Params: mustJSON(map[string]any{
		"threadId": threadID, "turnId": turnID, "delta": largeDelta,
	})})
	if err != nil || len(largeFrame) <= 64<<10 {
		t.Fatalf("large frame size=%d err=%v, want >64 KiB", len(largeFrame), err)
	}

	resumed, err := h.client.Open(ctx, cwd, "read-only", "", threadID)
	if err != nil || resumed != threadID {
		t.Fatalf("thread/resume id=%q err=%v", resumed, err)
	}
	if err := h.client.Interrupt(ctx, threadID, turnID); err != nil {
		t.Fatalf("turn/interrupt: %v", err)
	}

	trace := readMockTrace(t, h.trace)
	assertNumericCalls(t, trace, []string{"initialize", "initialized", "thread/start", "turn/start", "thread/resume", "turn/interrupt"})
	if len(trace) < 3 || trace[0].Kind != "client" || trace[0].Method != "initialize" || trace[1].Kind != "server" || trace[1].Method != "initialize" || trace[2].Kind != "client" || trace[2].Method != "initialized" {
		t.Fatalf("initialize response was not followed by initialized notification: %#v", trace[:min(len(trace), 3)])
	}
}

func TestOpenRejectsUnexpectedEffectiveSandboxPolicy(t *testing.T) {
	h := startMockClient(t, "unsafe-policy")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := h.client.Open(ctx, cwd, "read-only", "", "")
	if err == nil {
		t.Fatalf("thread/start accepted dangerFullAccess sandbox policy: threadID=%q", threadID)
	}
	if threadID != "" {
		t.Fatalf("thread/start returned thread id %q despite policy mismatch", threadID)
	}
}

func TestUnknownServerRequestGetsMethodNotFoundWithoutAllow(t *testing.T) {
	h := startMockClient(t, "unknown")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.process.Wait(ctx); err != nil {
		t.Fatalf("fixture did not exit after unsupported request: %v", err)
	}
	trace := readMockTrace(t, h.trace)
	var rejected *mockTrace
	for i := range trace {
		entry := &trace[i]
		if entry.Kind == "client" && entry.Method == "" && len(entry.Error) != 0 {
			rejected = entry
			break
		}
	}
	if rejected == nil {
		t.Fatal("client sent no RPC error for unsupported server request")
	}
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rejected.Error, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != -32601 || payload.Message != "Unsupported request" {
		t.Fatalf("unsupported request response=%s", rejected.Error)
	}
}

func TestApprovalRejectsAcceptForSession(t *testing.T) {
	h := startMockClient(t, "flow")
	if err := h.client.Approval(json.RawMessage(`9002`), "acceptForSession"); err == nil || err.Error() != "UNSUPPORTED_APPROVAL" {
		t.Fatalf("Approval acceptForSession error=%v, want UNSUPPORTED_APPROVAL", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Open(ctx, cwd, "read-only", "", ""); err != nil {
		t.Fatalf("barrier thread/start: %v", err)
	}
	for _, entry := range readMockTrace(t, h.trace) {
		if entry.Kind == "client" && entry.Method == "" && bytes.Contains(entry.Result, []byte("acceptForSession")) {
			t.Fatal("acceptForSession was sent to the runtime")
		}
	}
}

func TestFrameOver8MiBReportsExplicitLimitError(t *testing.T) {
	h := startMockClient(t, "oversize")
	err := waitForClientFailure(t, h.client)
	if !strings.Contains(err.Error(), "OUTPUT_LIMIT") {
		t.Fatalf("oversized frame error=%v, want OUTPUT_LIMIT", err)
	}
}

func TestEventQueueOverLimitReportsExplicitLimitError(t *testing.T) {
	h := startMockClient(t, "queue")
	err := waitForClientFailure(t, h.client)
	if !strings.Contains(err.Error(), "OUTPUT_LIMIT") {
		t.Fatalf("queue overflow error=%v, want OUTPUT_LIMIT", err)
	}
}

func TestMegabyteStderrDoesNotBlockStdout(t *testing.T) {
	h := startMockClient(t, "stderr")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := h.client.Open(ctx, cwd, "read-only", "", "")
	if err != nil {
		t.Fatalf("stdout response blocked by stderr output: %v", err)
	}
	if threadID != "thread-fixture" {
		t.Fatalf("thread id=%q", threadID)
	}
}
