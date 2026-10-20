//go:build windows

// Package updater stages verified installers and applies them while the profile
// is offline. Development builds cannot bypass the operating system trust check.
package updater

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pokegents/dashboard/server/desktop"
)

const maxInstaller int64 = 256 << 20

type Artifact struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}
type Manifest struct {
	ExecutableSHA256 string    `json:"executable_sha256"`
	Schema           int       `json:"schema_version"`
	Product          string    `json:"product"`
	Target           string    `json:"target"`
	Installer        Artifact  `json:"installer"`
	Rollback         *Artifact `json:"rollback,omitempty"`
}
type Stage struct {
	Manifest       Manifest `json:"manifest"`
	Pin            string   `json:"signer_sha256"`
	CurrentVersion string   `json:"current_version"`
	CurrentHash    string   `json:"current_sha256"`
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func version(value string) ([3]int, error) {
	var out [3]int
	if !versionPattern.MatchString(value) {
		return out, errors.New("UPDATE_STABLE_VERSION_REQUIRED")
	}
	for i, p := range strings.Split(value, ".") {
		n, e := strconv.Atoi(p)
		if e != nil || n > 65535 {
			return out, errors.New("UPDATE_INVALID_VERSION")
		}
		out[i] = n
	}
	return out, nil
}
func newer(next, current string) bool {
	a, e := version(next)
	b, f := version(current)
	if e != nil || f != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
func hexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && s == strings.ToLower(s)
}
func safeURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, errors.New("UPDATE_HTTPS_URL_REQUIRED")
	}
	return u, nil
}
func downloadClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("UPDATE_REDIRECT_REFUSED") }}
}
func fetch(ctx context.Context, client *http.Client, raw string, limit int64, writer io.Writer) error {
	if _, err := safeURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("UPDATE_DOWNLOAD_FAILED")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit || response.Header.Get("Content-Encoding") != "" {
		return errors.New("UPDATE_DOWNLOAD_FAILED")
	}
	n, err := io.Copy(writer, io.LimitReader(response.Body, limit+1))
	if err != nil || n > limit {
		return errors.New("UPDATE_DOWNLOAD_LIMIT")
	}
	return nil
}
func validate(m Manifest, current, source string) error {
	origin, err := safeURL(source)
	if err != nil {
		return err
	}
	if m.Schema != 1 || m.Product != "Pokegents" || m.Target != "windows/amd64" || !hexHash(m.ExecutableSHA256) || !newer(m.Installer.Version, current) {
		return errors.New("UPDATE_MANIFEST_INVALID")
	}
	for i, a := range []*Artifact{&m.Installer, m.Rollback} {
		if a == nil {
			continue
		}
		u, err := safeURL(a.URL)
		if err != nil || u.Host != origin.Host || !hexHash(a.SHA256) || a.Size <= 0 || a.Size > maxInstaller {
			return errors.New("UPDATE_MANIFEST_INVALID")
		}
		if _, err := version(a.Version); err != nil {
			return err
		}
		if i == 1 && a.Version != current {
			return errors.New("UPDATE_ROLLBACK_VERSION_MISMATCH")
		}
	}
	return nil
}
func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func checkFile(ctx context.Context, path string, a Artifact, pin string, verify func(context.Context, string) (signature, error)) error {
	if err := desktop.CheckDirectLocalPath(path); err != nil {
		return err
	}
	f, e := os.Stat(path)
	if e != nil || !f.Mode().IsRegular() || f.Size() != a.Size {
		return errors.New("UPDATE_SIZE_MISMATCH")
	}
	hash, e := hashFile(path)
	if e != nil || hash != a.SHA256 {
		return errors.New("UPDATE_HASH_MISMATCH")
	}
	s, e := verify(ctx, path)
	if e != nil || s.Pin != pin || s.Product != "Pokegents" || (s.Version != a.Version && s.Version != a.Version+".0") {
		return errors.New("UPDATE_SIGNATURE_OR_PRODUCT_MISMATCH")
	}
	return nil
}
func writeNew(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}
func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	if err = dec.Decode(value); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("UPDATE_INVALID_JSON")
	}
	return nil
}

func Check(ctx context.Context, exe, current, source string) (Manifest, string, error) {
	if _, err := version(current); err != nil {
		return Manifest{}, "", err
	}
	s, err := verifySignature(ctx, exe)
	if err != nil {
		return Manifest{}, "", err
	}
	var b strings.Builder
	if err := fetch(ctx, downloadClient(), source, 65536, &b); err != nil {
		return Manifest{}, "", err
	}
	var m Manifest
	decoder := json.NewDecoder(strings.NewReader(b.String()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return m, "", errors.New("UPDATE_MANIFEST_INVALID")
	}
	if err := validate(m, current, source); err != nil {
		return m, "", err
	}
	return m, s.Pin, nil
}
func Prepare(ctx context.Context, profile, exe, current, source string) (string, error) {
	m, pin, err := Check(ctx, exe, current, source)
	if err != nil {
		return "", err
	}
	if m.Rollback == nil {
		return "", errors.New("UPDATE_ROLLBACK_REQUIRED")
	}
	if err := desktop.PrepareProfile(profile); err != nil {
		return "", err
	}
	root := filepath.Join(profile, "updates")
	if err := desktop.CheckDirectLocalPath(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	folder, err := os.MkdirTemp(root, "stage-")
	if err != nil {
		return "", err
	}
	for i, a := range []Artifact{m.Installer, *m.Rollback} {
		name := "installer.exe"
		if i == 1 {
			name = "rollback.exe"
		}
		p := filepath.Join(folder, name)
		f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		e = fetch(ctx, downloadClient(), a.URL, a.Size, f)
		syncErr := f.Sync()
		closeErr := f.Close()
		if e != nil {
			return "", e
		}
		if syncErr != nil {
			return "", syncErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if e = checkFile(ctx, p, a, pin, verifySignature); e != nil {
			return "", e
		}
	}
	hash, err := hashFile(exe)
	if err != nil {
		return "", err
	}
	err = writeNew(filepath.Join(folder, "stage.json"), Stage{m, pin, current, hash})
	return folder, err
}
