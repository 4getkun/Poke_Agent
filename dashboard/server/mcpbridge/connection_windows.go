//go:build windows

package mcpbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
)

// Connection contains a dedicated, revocable MCP credential, never the UI token.
type Connection struct {
	Version int    `json:"version"`
	Origin  string `json:"origin"`
	Token   string `json:"token"`
	Scope   string `json:"scope"`
}

func privateDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + u.User.Sid.String() + ")")
}

func checkPrivateFile(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	expected, err := privateDescriptor()
	if err != nil {
		return err
	}
	actual, want := sd.String(), expected.String()
	i, j := strings.Index(actual, "("), strings.Index(want, "(")
	if i < 0 || j < 0 || !strings.HasPrefix(actual, "D:P") || actual[i:] != want[j:] {
		return errors.New("MCP_CONNECTION_PERMISSIONS_UNSAFE")
	}
	return nil
}

func validateConnection(c Connection) error {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("MCP_CONNECTION_ORIGIN_INVALID")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || c.Version != 1 {
		return errors.New("MCP_CONNECTION_INVALID")
	}
	if c.Scope != "read" && c.Scope != "task-write" {
		return errors.New("MCP_CONNECTION_SCOPE_INVALID")
	}
	b, err := hex.DecodeString(c.Token)
	if err != nil || len(b) != 32 {
		return errors.New("MCP_CONNECTION_CREDENTIAL_INVALID")
	}
	return nil
}

func ReadConnection(path string) (Connection, error) {
	var c Connection
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return c, errors.New("MCP_CONNECTION_PATH_INVALID")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return c, errors.New("MCP_CONNECTION_UNAVAILABLE")
	}
	if err := checkPrivateFile(path); err != nil {
		return c, err
	}
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("MCP_CONNECTION_UNAVAILABLE")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 8193))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Connection{}, errors.New("MCP_CONNECTION_INVALID")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return Connection{}, errors.New("MCP_CONNECTION_INVALID")
	}
	return c, validateConnection(c)
}

// WriteConnection writes only inside a caller-prepared, owner-only app folder.
func WriteConnection(path string, c Connection) error {
	if err := validateConnection(c); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return errors.New("MCP_CONNECTION_PATH_INVALID")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".connection-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sd, err := privateDescriptor()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err = windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(c); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

var bridgeHTTP = &http.Client{
	Timeout:       15 * time.Second,
	Transport:     &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// HTTPGateway reloads the private connection file so a running MCP client can
// reconnect after the app changes its ephemeral loopback port.
func HTTPGateway(path string) Gateway {
	return func(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		c, err := ReadConnection(path)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", c.Origin+"/api/mcp/tools/"+url.PathEscape(name), bytes.NewReader(input))
		if err != nil {
			return nil, errors.New("MCP_REQUEST_INVALID")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pokegents-MCP-Token", c.Token)
		resp, err := bridgeHTTP.Do(req)
		if err != nil {
			return nil, errors.New("MCP_APP_UNAVAILABLE: Pokegentsを起動してください")
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		if err != nil || len(body) > 8<<20 {
			return nil, errors.New("MCP_RESPONSE_LIMIT")
		}
		if resp.StatusCode != http.StatusOK {
			var failure struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(body, &failure) == nil && len(failure.Code) > 0 && len(failure.Code) <= 80 && !strings.ContainsAny(failure.Code, "\r\n") {
				return nil, fmt.Errorf("Pokegents: %s", failure.Code)
			}
			return nil, fmt.Errorf("MCP_REQUEST_REJECTED: HTTP %d", resp.StatusCode)
		}
		if !json.Valid(body) {
			return nil, errors.New("MCP_RESPONSE_INVALID")
		}
		return body, nil
	}
}

func RunCLI(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("connection", "", "Private Pokegents MCP connection file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("usage: Pokegents.exe mcp --connection <absolute-path>")
	}
	c, err := ReadConnection(*path)
	if err != nil {
		return err
	}
	server := NewServer(HTTPGateway(*path), c.Scope == "task-write")
	return server.Run(ctx, &mcp.StdioTransport{MaxLineLength: (1 << 20) + 8192})
}
