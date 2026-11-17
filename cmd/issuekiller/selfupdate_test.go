package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.3.0", "v1.2.9", 1},
		{"v1.2.3", "v1.2.10", -1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.2.3-rc1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-rc1", 1},
		{"v1.2.3-beta", "v1.2.3-alpha", 1},
		{"dev", "v1.2.3", -1},
		{"v1.2.3+build", "v1.2.3", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	if !isNewerVersion("v0.2.0", "v0.1.0") {
		t.Fatal("0.2.0 should be newer than 0.1.0")
	}
	if isNewerVersion("v0.1.0", "v0.1.0") {
		t.Fatal("equal versions are not newer")
	}
	if !isNewerVersion("v0.1.0", "dev") {
		t.Fatal("a release is newer than a local dev build")
	}
	if isNewerVersion("not-a-version", "dev") {
		t.Fatal("an unparsable tag must not trigger an update")
	}
}

func TestPickReleaseAsset(t *testing.T) {
	assets := []releaseAsset{
		{Name: "checksums.txt"},
		{Name: "issuekiller-desktop_v1.2.3_darwin_arm64.zip"},
		{Name: "issuekiller_v1.2.3_darwin_arm64.tar.gz"},
		{Name: "issuekiller_v1.2.3_linux_amd64.tar.gz"},
		{Name: "issuekiller_v1.2.3_windows_amd64.zip"},
	}
	asset, ok := pickReleaseAsset(assets, "v1.2.3", "darwin", "arm64")
	if !ok || asset.Name != "issuekiller_v1.2.3_darwin_arm64.tar.gz" {
		t.Fatalf("darwin asset = %+v ok=%v", asset, ok)
	}
	asset, ok = pickReleaseAsset(assets, "v1.2.3", "windows", "amd64")
	if !ok || asset.Name != "issuekiller_v1.2.3_windows_amd64.zip" {
		t.Fatalf("windows asset = %+v ok=%v", asset, ok)
	}
	if _, ok := pickReleaseAsset(assets, "v1.2.3", "linux", "arm64"); ok {
		t.Fatal("missing platform must not match the desktop bundle")
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "d41d8cd98f00b204e9800998ecf8427e  other.tar.gz\n" +
		"  abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  *dir/issuekiller_v1.0.0_linux_amd64.tar.gz\n" +
		"not-a-hash  issuekiller_x.tar.gz\n"
	hash, ok := checksumFor("issuekiller_v1.0.0_linux_amd64.tar.gz", sums)
	if !ok || hash != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("hash=%q ok=%v", hash, ok)
	}
	if _, ok := checksumFor("issuekiller_x.tar.gz", sums); ok {
		t.Fatal("invalid hash lines must be ignored")
	}
	if _, ok := checksumFor("missing.tar.gz", sums); ok {
		t.Fatal("unknown assets have no checksum")
	}
}

func TestMirrorURL(t *testing.T) {
	raw := "https://github.com/o/r/releases/download/v1/a.tar.gz"
	if got := mirrorURL("https://gh-proxy.com/", raw); got != "https://gh-proxy.com/"+raw {
		t.Fatalf("mirror = %q", got)
	}
	if got := mirrorURL("", raw); got != raw {
		t.Fatalf("empty mirror must keep the url, got %q", got)
	}
}

func TestNormalizeMirror(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"none":                     "",
		"gh-proxy.com":             "https://gh-proxy.com",
		"https://gh-proxy.com/":    "https://gh-proxy.com",
		"http://127.0.0.1:8080/x/": "http://127.0.0.1:8080/x",
	}
	for raw, want := range cases {
		got, err := normalizeMirror(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeMirror(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, invalid := range []string{"ftp://gh-proxy.com", "https://"} {
		if _, err := normalizeMirror(invalid); err == nil {
			t.Fatalf("normalizeMirror(%q) should fail", invalid)
		}
	}
}

func TestAssetNameFor(t *testing.T) {
	if got := assetNameFor("v1.2.3", "linux", "arm64"); got != "issuekiller_v1.2.3_linux_arm64.tar.gz" {
		t.Fatalf("linux name = %q", got)
	}
	if got := assetNameFor("v1.2.3", "windows", "amd64"); got != "issuekiller_v1.2.3_windows_amd64.zip" {
		t.Fatalf("windows name = %q", got)
	}
}

func writeTarGz(t *testing.T, path, name string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path, name string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("#!/bin/sh\necho fresh\n")
	tarPath := filepath.Join(dir, "release.tar.gz")
	writeTarGz(t, tarPath, "issuekiller", payload)
	zipPath := filepath.Join(dir, "release.zip")
	writeZip(t, zipPath, "nested/issuekiller.exe", payload)

	for _, archive := range []string{tarPath, zipPath} {
		dest := filepath.Join(dir, "out-"+filepath.Base(archive))
		if err := extractBinary(archive, dest); err != nil {
			t.Fatalf("%s: %v", archive, err)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, payload) {
			t.Fatalf("%s: extracted %q", archive, data)
		}
	}

	empty := filepath.Join(dir, "empty.tar.gz")
	writeTarGz(t, empty, "README.md", []byte("no binary"))
	if err := extractBinary(empty, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("an archive without the binary must fail")
	}
}

// releaseServer serves one archive and a checksums.txt, so install can be
// exercised without GitHub.
func releaseServer(t *testing.T, archive []byte, assetName, wantHash string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/asset":
			_, _ = rw.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(rw, "%s  %s\n", wantHash, assetName)
		default:
			http.NotFound(rw, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestInstallReleaseRejectsBadChecksum(t *testing.T) {
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = "" })

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "release.tar.gz")
	writeTarGz(t, archivePath, "issuekiller", []byte("new binary"))
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	name := assetNameFor("v1.2.3", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		archivePath = filepath.Join(dir, "release.zip")
		writeZip(t, archivePath, "issuekiller.exe", []byte("new binary"))
		archive, err = os.ReadFile(archivePath)
		if err != nil {
			t.Fatal(err)
		}
	}
	server := releaseServer(t, archive, name, strings.Repeat("0", 64))
	release := &releaseInfo{TagName: "v1.2.3", Assets: []releaseAsset{
		{Name: name, URL: server.URL + "/asset"},
		{Name: "checksums.txt", URL: server.URL + "/checksums.txt"},
	}}

	exe := filepath.Join(dir, "issuekiller")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = installReleaseTo(context.Background(), NewSilentLogger(), release, "", "", exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	data, readErr := os.ReadFile(exe)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old binary" {
		t.Fatalf("running binary was replaced: %q", data)
	}
}

func TestInstallReleaseInstallsVerifiedAsset(t *testing.T) {
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = "" })

	dir := t.TempDir()
	payload := []byte("verified binary")
	name := assetNameFor("v1.2.3", runtime.GOOS, runtime.GOARCH)
	var archive []byte
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "release.zip")
		writeZip(t, path, "issuekiller.exe", payload)
		archive, _ = os.ReadFile(path)
	} else {
		path := filepath.Join(dir, "release.tar.gz")
		writeTarGz(t, path, "issuekiller", payload)
		archive, _ = os.ReadFile(path)
	}
	sum := sha256.Sum256(archive)
	server := releaseServer(t, archive, name, hex.EncodeToString(sum[:]))
	release := &releaseInfo{TagName: "v1.2.3", Assets: []releaseAsset{
		{Name: name, URL: server.URL + "/asset"},
		{Name: "checksums.txt", URL: server.URL + "/checksums.txt"},
	}}

	exe := filepath.Join(dir, "issuekiller")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installReleaseTo(context.Background(), NewSilentLogger(), release, "", "", exe); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("installed binary = %q", data)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("staged file must not be left behind")
	}
}

// A restart that was queued behind a long job can re-exec an older build; the
// watcher must then see the newer master instead of adopting it as baseline.
func TestMasterWatchBaselineUsesBuildCommit(t *testing.T) {
	cases := []struct{ head, build, want string }{
		{"bbb", "", "bbb"},
		{"bbb", "aaa", "aaa"},
		{"bbb", "bbb", "bbb"},
	}
	for _, c := range cases {
		if got := masterWatchBaseline(c.head, c.build); got != c.want {
			t.Fatalf("masterWatchBaseline(%q, %q) = %q, want %q", c.head, c.build, got, c.want)
		}
	}
}
