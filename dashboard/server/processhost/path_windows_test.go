//go:build windows

package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type processPathResult struct {
	Args []string `json:"args"`
	CWD  string   `json:"cwd"`
}

func TestProcessUnicodePathFixture(t *testing.T) {
	if os.Getenv("POKEGENTS_PROCESS_PATH_FIXTURE") != "1" {
		return
	}
	result := processPathResult{Args: os.Args, CWD: mustGetwd()}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "PATH_RESULT:%s\n", b)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}

func TestStartPreservesUnicodeAndSpecialExecutableArgumentAndWorkingPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "日本語 path &()[]")
	workDir := filepath.Join(root, "作業 folder &()[]")
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(root, "検査 executable &()[] .exe")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := os.Create(fixturePath)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	if _, err = io.Copy(destination, source); err != nil {
		source.Close()
		destination.Close()
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		destination.Close()
		t.Fatal(err)
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}

	wantArg := "引数 with spaces &()[]"
	env := append(Environment(), "POKEGENTS_PROCESS_PATH_FIXTURE=1")
	process, err := Start(fixturePath, []string{"-test.run=^TestProcessUnicodePathFixture$", "-test.v", wantArg}, workDir, env)
	if err != nil {
		t.Fatalf("Start(exe=%q, cwd=%q): %v", fixturePath, workDir, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer process.Close(ctx)
	output, err := io.ReadAll(process.Stdout)
	if err != nil {
		t.Fatalf("read child stdout: %v", err)
	}
	if err = process.Wait(ctx); err != nil {
		t.Fatalf("wait for child: %v; output=%s", err, output)
	}

	var got processPathResult
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PATH_RESULT:") {
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "PATH_RESULT:")), &got); err != nil {
				t.Fatalf("decode child result: %v", err)
			}
			break
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatalf("scan child stdout: %v", err)
	}
	if got.CWD != workDir {
		t.Fatalf("child cwd=%q, want %q", got.CWD, workDir)
	}
	foundArg := false
	for _, arg := range got.Args {
		if arg == wantArg {
			foundArg = true
			break
		}
	}
	if !foundArg {
		t.Fatalf("child args %q do not contain original argument %q", got.Args, wantArg)
	}
}
