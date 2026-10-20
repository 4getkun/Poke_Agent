//go:build windows

// Package runtimeadapter implements the pinned Codex app-server stdio protocol.
package runtimeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"pokegents/dashboard/server/processhost"
)

const SupportedVersion = "0.163.0"
const MaxInput = 1 << 20
const MaxFrame = 8 << 20
const MaxQueue = 8 << 20

type Frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type Event struct {
	Frame Frame
	bytes int
}
type Client struct {
	process  *processhost.Process
	write    sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan Frame
	seq      atomic.Uint64
	events   chan Event
	queued   atomic.Int64
	done     chan struct{}
	readers  sync.WaitGroup
	failOnce sync.Once
	err      error
}

func Start(ctx context.Context, exe, cwd string) (*Client, error) {
	p, err := processhost.Start(exe, []string{"app-server", "--listen", "stdio://"}, cwd, processhost.Environment())
	return initialize(ctx, p, err)
}

func StartOwned(ctx context.Context, exe, cwd string, owner processhost.Ownership) (*Client, error) {
	p, err := processhost.StartOwned(exe, []string{"app-server", "--listen", "stdio://"}, cwd, processhost.Environment(), owner)
	return initialize(ctx, p, err)
}

func initialize(ctx context.Context, p *processhost.Process, err error) (*Client, error) {
	if err != nil {
		return nil, err
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
	var result json.RawMessage
	err = c.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "pokegents_windows", "title": "Pokegents for Windows", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": false}}, &result)
	if err == nil {
		err = c.Notify("initialized", map[string]any{})
	}
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.Close(closeCtx)
		return nil, err
	}
	return c, nil
}
func (c *Client) fail(err error) {
	c.failOnce.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.done) })
}
func (c *Client) read() {
	defer c.readers.Done()
	defer close(c.events)
	scanner := bufio.NewScanner(c.process.Stdout)
	scanner.Buffer(make([]byte, 64*1024), MaxFrame+1)
	for scanner.Scan() {
		b := scanner.Bytes()
		if len(b) > MaxFrame {
			c.fail(errors.New("OUTPUT_LIMIT"))
			return
		}
		var f Frame
		if err := json.Unmarshal(b, &f); err != nil {
			c.fail(errors.New("PROTOCOL_ERROR"))
			return
		}
		if len(f.ID) > 0 && f.Method == "" {
			c.mu.Lock()
			ch := c.pending[string(f.ID)]
			c.mu.Unlock()
			if ch == nil {
				c.fail(errors.New("PROTOCOL_ERROR: unmatched response"))
				return
			}
			select {
			case ch <- f:
			default:
				c.fail(errors.New("PROTOCOL_ERROR: duplicate response"))
				return
			}
			continue
		}
		if f.Method == "" {
			c.fail(errors.New("PROTOCOL_ERROR: missing method"))
			return
		}
		// Unsupported server requests never become implicit permission grants.
		if len(f.ID) > 0 && f.Method != "item/commandExecution/requestApproval" && f.Method != "item/fileChange/requestApproval" {
			if err := c.send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32601, "message": "Unsupported request"}}); err != nil {
				c.fail(err)
				return
			}
			continue
		}
		owned := len(b)
		if c.queued.Add(int64(owned)) > MaxQueue {
			c.fail(errors.New("OUTPUT_LIMIT"))
			return
		}
		e := Event{Frame: f, bytes: owned}
		select {
		case c.events <- e:
		default:
			c.queued.Add(-int64(owned))
			c.fail(errors.New("OUTPUT_LIMIT"))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("OUTPUT_LIMIT or runtime pipe error: %w", err))
	} else {
		c.fail(io.EOF)
	}
}
func (c *Client) Next(ctx context.Context) (Frame, error) {
	select {
	case e, ok := <-c.events:
		if ok {
			c.queued.Add(-int64(e.bytes))
			return e.Frame, nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return Frame{}, c.err
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}
func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxInput {
		return errors.New("INPUT_LIMIT")
	}
	c.write.Lock()
	defer c.write.Unlock()
	select {
	case <-c.done:
		return errors.New("runtime closed")
	default:
	}
	_, err = c.process.Stdin.Write(append(b, '\n'))
	return err
}
func (c *Client) Notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	id := c.seq.Add(1)
	key := fmt.Sprint(id)
	ch := make(chan Frame, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case f := <-ch:
		if len(f.Error) > 0 {
			return errors.New("PROTOCOL_ERROR: RPC rejected")
		}
		if out != nil {
			return json.Unmarshal(f.Result, out)
		}
		return nil
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	}
}
func (c *Client) Approval(id json.RawMessage, decision string) error {
	if decision != "accept" && decision != "decline" {
		return errors.New("UNSUPPORTED_APPROVAL")
	}
	return c.send(map[string]any{"id": id, "result": map[string]any{"decision": decision}})
}
func (c *Client) Open(ctx context.Context, cwd, policy, model, threadID string) (string, error) {
	if policy != "read-only" && policy != "workspace-write" {
		return "", errors.New("INVALID_POLICY")
	}
	approvalPolicy := "untrusted"
	if policy == "read-only" {
		approvalPolicy = "never"
	}
	params := map[string]any{"cwd": cwd, "sandbox": policy, "approvalPolicy": approvalPolicy}
	if model != "" {
		params["model"] = model
	}
	method := "thread/start"
	if threadID != "" {
		method = "thread/resume"
		params["threadId"] = threadID
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ApprovalPolicy string `json:"approvalPolicy"`
		Sandbox        struct {
			Type string `json:"type"`
		} `json:"sandbox"`
	}
	if err := c.Call(ctx, method, params, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" {
		return "", errors.New("PROTOCOL_ERROR: missing thread id")
	}
	expectedSandbox := "readOnly"
	if policy == "workspace-write" {
		expectedSandbox = "workspaceWrite"
	}
	if response.ApprovalPolicy != approvalPolicy || response.Sandbox.Type != expectedSandbox {
		return "", errors.New("UNSUPPORTED_RUNTIME: effective policy mismatch")
	}
	return response.Thread.ID, nil
}
func (c *Client) Turn(ctx context.Context, threadID, input string) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := c.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": input}}}, &response)
	if err != nil {
		return "", err
	}
	if response.Turn.ID == "" {
		return "", errors.New("PROTOCOL_ERROR: missing turn id")
	}
	return response.Turn.ID, nil
}
func (c *Client) Interrupt(ctx context.Context, threadID, turnID string) error {
	return c.Call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}
func (c *Client) Close(ctx context.Context) error {
	c.fail(errors.New("runtime closed"))
	err := c.process.Close(ctx)
	waited := make(chan struct{})
	go func() { c.readers.Wait(); close(waited) }()
	select {
	case <-waited:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Probe runs only the selected native exe, within the same atomic Job host.
func Probe(ctx context.Context, exe, cwd string) (string, error) {
	p, err := processhost.Start(exe, []string{"--version"}, cwd, processhost.Environment())
	if err != nil {
		return "", err
	}
	defer p.Close(ctx)
	var stdout, stderr []byte
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); stdout, e1 = io.ReadAll(io.LimitReader(p.Stdout, 4097)) }()
	go func() { defer wg.Done(); stderr, e2 = io.ReadAll(io.LimitReader(p.Stderr, 4097)) }()
	if err = p.Wait(ctx); err != nil {
		return "", err
	}
	wg.Wait()
	if e1 != nil || e2 != nil || len(stdout) > 4096 || len(stderr) > 4096 {
		return "", errors.New("OUTPUT_LIMIT")
	}
	if string(stdout) != "codex-cli "+SupportedVersion+"\n" && string(stdout) != "codex-cli "+SupportedVersion+"\r\n" {
		return "", errors.New("UNSUPPORTED_RUNTIME")
	}
	return SupportedVersion, nil
}
