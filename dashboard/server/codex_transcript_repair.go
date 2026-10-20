package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// repairCodexTranscriptMissingCustomToolOutputs patches a native Codex JSONL
// transcript that contains a custom_tool_call without a matching
// custom_tool_call_output. Codex core logs this as:
//
//	Custom tool call output is missing for call id: ...
//
// This usually happens when the dashboard/backend is killed after Codex records
// the custom tool call but before it records the tool result. The orphan then
// pollutes every future turn for that session. We repair only custom-tool calls
// and insert a clearly synthetic output immediately after the orphaned call.
func repairCodexTranscriptMissingCustomToolOutputs(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("codex transcript path is a directory: %s", path)
	}

	outputs, err := scanCodexCustomToolOutputs(path)
	if err != nil {
		return 0, err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".repair-*.jsonl")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	in, err := os.Open(path)
	if err != nil {
		_ = tmp.Close()
		return 0, err
	}
	defer in.Close()

	reader := bufio.NewReader(in)
	writer := bufio.NewWriter(tmp)
	repaired := 0
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			if _, err := writer.WriteString(line); err != nil {
				_ = tmp.Close()
				return 0, err
			}
			if !strings.HasSuffix(line, "\n") {
				if err := writer.WriteByte('\n'); err != nil {
					_ = tmp.Close()
					return 0, err
				}
			}
			if info, ok := parseCodexCustomToolCallLine(line); ok {
				if !outputs[info.CallID] {
					synthetic, err := syntheticCodexCustomToolOutputLine(info)
					if err != nil {
						_ = tmp.Close()
						return 0, err
					}
					if _, err := writer.WriteString(synthetic + "\n"); err != nil {
						_ = tmp.Close()
						return 0, err
					}
					outputs[info.CallID] = true
					repaired++
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = tmp.Close()
			return 0, readErr
		}
	}
	if err := writer.Flush(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	// Windows does not permit replacing a file while this read handle is open.
	if err := in.Close(); err != nil {
		return 0, err
	}
	if repaired == 0 {
		return 0, nil
	}

	backup := fmt.Sprintf("%s.bak.missing-tool-output.%s", path, time.Now().Format("20060102-150405"))
	if err := copyFile(path, backup); err != nil {
		return 0, fmt.Errorf("backup codex transcript: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, err
	}
	return repaired, nil
}

type codexCustomToolCallLine struct {
	CallID    string
	Name      string
	Timestamp string
}

func scanCodexCustomToolOutputs(path string) (map[string]bool, error) {
	outputs := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return outputs, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			if id, ok := parseCodexCustomToolOutputLine(line); ok {
				outputs[id] = true
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return outputs, readErr
		}
	}
	return outputs, nil
}

func parseCodexCustomToolCallLine(line string) (codexCustomToolCallLine, bool) {
	var zero codexCustomToolCallLine
	if !strings.Contains(line, `"custom_tool_call"`) || strings.Contains(line, `"custom_tool_call_output"`) {
		return zero, false
	}
	var frame struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &frame); err != nil {
		return zero, false
	}
	if frame.Type != "response_item" || frame.Payload.Type != "custom_tool_call" || frame.Payload.CallID == "" {
		return zero, false
	}
	return codexCustomToolCallLine{CallID: frame.Payload.CallID, Name: frame.Payload.Name, Timestamp: frame.Timestamp}, true
}

func parseCodexCustomToolOutputLine(line string) (string, bool) {
	if !strings.Contains(line, `"custom_tool_call_output"`) {
		return "", false
	}
	var frame struct {
		Type    string `json:"type"`
		Payload struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &frame); err != nil {
		return "", false
	}
	if frame.Type != "response_item" || frame.Payload.Type != "custom_tool_call_output" || frame.Payload.CallID == "" {
		return "", false
	}
	return frame.Payload.CallID, true
}

func syntheticCodexCustomToolOutputLine(info codexCustomToolCallLine) (string, error) {
	ts := info.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	output, err := json.Marshal(map[string]any{
		"output": fmt.Sprintf("[pokegents repaired transcript: output for %s was missing after an interrupted backend/tool turn; original result unavailable]", info.Name),
		"metadata": map[string]any{
			"repaired": true,
			"source":   "pokegents",
		},
	})
	if err != nil {
		return "", err
	}
	frame := map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call_output",
			"call_id": info.CallID,
			"output":  string(output),
		},
	}
	b, err := json.Marshal(frame)
	return string(b), err
}
