//go:build windows

package updater

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/processhost"
)

type signature struct {
	Pin     string `json:"pin"`
	Product string `json:"product"`
	Version string `json:"version"`
}

// A fixed, profile-free OS script performs Authenticode verification. No path or
// release text is interpreted as script; only the explicit environment is read.
const trustScript = `$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue';
$f=$env:POKEGENTS_VERIFY_FILE;
$s=Get-AuthenticodeSignature -LiteralPath $f;
if($s.Status -ne 'Valid' -or $null -eq $s.SignerCertificate){exit 4};
$eku=@($s.SignerCertificate.EnhancedKeyUsageList | ForEach-Object {[string]$_.ObjectId});
if($eku -notcontains '1.3.6.1.5.5.7.3.3'){exit 5};
$sha=[System.Security.Cryptography.SHA256]::Create();
$pin=([BitConverter]::ToString($sha.ComputeHash($s.SignerCertificate.RawData))).Replace('-','').ToLowerInvariant();
$v=[System.Diagnostics.FileVersionInfo]::GetVersionInfo($f);
@{pin=$pin;product=$v.ProductName;version=$v.ProductVersion}|ConvertTo-Json -Compress`

func verifySignature(ctx context.Context, path string) (result signature, err error) {
	root, err := windows.GetSystemDirectory()
	if err != nil {
		return result, err
	}
	units := utf16.Encode([]rune(trustScript))
	data := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(data[i*2:], u)
	}
	p, err := processhost.Start(filepath.Join(root, "WindowsPowerShell", "v1.0", "powershell.exe"), []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}, filepath.Dir(path), append(processhost.Environment(), "POKEGENTS_VERIFY_FILE="+path))
	if err != nil {
		return result, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := p.Close(closeCtx); closeErr != nil {
			result = signature{}
			err = errors.New("UPDATE_SIGNATURE_PROCESS_NOT_QUIET")
		}
	}()
	output := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(io.LimitReader(p.Stdout, 16385)); output <- b }()
	go io.Copy(io.Discard, p.Stderr)
	if err := p.Wait(ctx); err != nil {
		return result, errors.New("UPDATE_SIGNATURE_INVALID")
	}
	var b []byte
	select {
	case b = <-output:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	if len(b) > 16384 || json.Unmarshal(b, &result) != nil || !hexHash(result.Pin) {
		return signature{}, errors.New("UPDATE_SIGNATURE_INVALID")
	}
	return result, nil
}
