package main

// Self-update installs a GitHub release asset over the running binary. The
// previous implementation cloned master and ran `go build`, which needed Go and
// git on the user's machine and could not work behind the Great Firewall; a
// release archive plus checksums.txt is enough, and a mirror prefix (such as
// gh-proxy) covers the download when github.com is unreachable.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// commit is injected by the release build (-X main.commit=...) and shown by the
// startup log; it is not used to decide whether an update exists.
var commit = ""

// buildLabel is the version plus the short commit for release builds.
func buildLabel() string {
	if commit == "" {
		return version
	}
	return version + "+" + shortSHA(commit)
}

const (
	selfRepo     = "uiautodev/issuekiller"
	selfReleases = "https://api.github.com/repos/" + selfRepo + "/releases/latest"
	// selfCheck is deliberately slow: releases are cut by hand, and the
	// unauthenticated GitHub API allows 60 requests per hour per IP.
	selfCheck = 30 * time.Minute
	// selfMaxAsset caps a download so a wrong URL cannot fill the disk.
	selfMaxAsset = 200 << 20
)

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	HTMLURL string         `json:"html_url"`
	Body    string         `json:"body"`
	Assets  []releaseAsset `json:"assets"`
}

// lookupLatestRelease reads releases/latest. The HTTP proxy (config.proxy) is
// honoured; the release mirror only covers asset downloads.
func lookupLatestRelease(ctx context.Context, proxyRaw string) (*releaseInfo, error) {
	proxy, err := parseProxy(proxyRaw)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, selfReleases, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "issuekiller")
	client := newHTTPClient(proxy, 30*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", selfRepo, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases answered HTTP %d", response.StatusCode)
	}
	var release releaseInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}
	if release.TagName == "" {
		return nil, errors.New("release has no tag")
	}
	return &release, nil
}

// parseVersion splits "v1.2.3-rc1+build" into its parts. A leading "v" and the
// build metadata are optional; a pre-release suffix sorts before the release.
func parseVersion(raw string) (parts [3]int, pre string, ok bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
	if index := strings.IndexByte(value, '+'); index >= 0 {
		value = value[:index]
	}
	if index := strings.IndexByte(value, '-'); index >= 0 {
		pre = value[index+1:]
		value = value[:index]
	}
	fields := strings.Split(value, ".")
	if len(fields) > len(parts) {
		fields = fields[:len(parts)]
	}
	if len(fields) == 0 {
		return parts, "", false
	}
	for index, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 0 {
			return parts, "", false
		}
		parts[index] = number
	}
	return parts, pre, true
}

// compareVersions returns -1, 0 or 1. Values it cannot parse sort first, so
// "dev" is always older than a release tag.
func compareVersions(a, b string) int {
	left, preLeft, okLeft := parseVersion(a)
	right, preRight, okRight := parseVersion(b)
	switch {
	case !okLeft && !okRight:
		return 0
	case !okLeft:
		return -1
	case !okRight:
		return 1
	}
	for index := range left {
		if left[index] != right[index] {
			if left[index] < right[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case preLeft == preRight:
		return 0
	case preLeft == "":
		return 1
	case preRight == "":
		return -1
	case preLeft < preRight:
		return -1
	}
	return 1
}

func isNewerVersion(latest, current string) bool {
	if _, _, ok := parseVersion(latest); !ok {
		return false
	}
	return compareVersions(latest, current) > 0
}

// assetNameFor is the archive name the release workflow publishes.
func assetNameFor(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("issuekiller_%s_%s_%s%s", version, goos, goarch, ext)
}

// pickReleaseAsset finds the daemon archive for one platform. Desktop bundles
// are named issuekiller-desktop_..., so the "issuekiller_" prefix keeps
// them out even when the OS/arch suffix matches.
func pickReleaseAsset(assets []releaseAsset, version, goos, goarch string) (releaseAsset, bool) {
	exact := assetNameFor(version, goos, goarch)
	for _, asset := range assets {
		if asset.Name == exact {
			return asset, true
		}
	}
	suffix := "_" + goos + "_" + goarch
	if goos == "windows" {
		suffix += ".zip"
	} else {
		suffix += ".tar.gz"
	}
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, "issuekiller_") && strings.HasSuffix(asset.Name, suffix) {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func findReleaseAsset(assets []releaseAsset, name string) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

// checksumFor reads a sha256sums-style file: "<hex>  <name>", where the name may
// carry a leading "*" (binary mode) or a directory prefix.
func checksumFor(name, sums string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != 64 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		candidate := strings.TrimPrefix(fields[len(fields)-1], "*")
		if filepath.Base(candidate) == name {
			return hash, true
		}
	}
	return "", false
}

// installRelease downloads the platform archive, verifies it against
// checksums.txt and replaces the running binary. Any failure leaves the old
// binary in place; the caller turns the return value into a log line.
func installRelease(ctx context.Context, log *Logger, release *releaseInfo, proxyRaw, mirrorRaw string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	return installReleaseTo(ctx, log, release, proxyRaw, mirrorRaw, exe)
}

// installReleaseTo is installRelease with the target binary injected so tests
// can prove that a bad checksum never replaces it.
func installReleaseTo(ctx context.Context, log *Logger, release *releaseInfo, proxyRaw, mirrorRaw, exe string) error {
	asset, ok := pickReleaseAsset(release.Assets, release.TagName, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("release %s has no asset for %s/%s", release.TagName, runtime.GOOS, runtime.GOARCH)
	}
	sumsAsset, ok := findReleaseAsset(release.Assets, "checksums.txt")
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt; refusing to install", release.TagName)
	}
	mirror, err := normalizeMirror(mirrorRaw)
	if err != nil {
		return err
	}
	proxy, err := parseProxy(proxyRaw)
	if err != nil {
		return err
	}
	client := newHTTPClient(proxy, 5*time.Minute)

	sums, err := downloadBytes(ctx, client, sumsAsset.URL, mirror, 1<<20)
	if err != nil {
		return fmt.Errorf("checksums.txt: %w", err)
	}
	expected, ok := checksumFor(asset.Name, string(sums))
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %s", asset.Name)
	}

	dir := filepath.Join(stateDir(), "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(dir, asset.Name)
	defer os.Remove(archive)
	downloaded, source, err := downloadFile(ctx, client, asset.URL, mirror, archive)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	if downloaded != expected {
		return fmt.Errorf("checksum mismatch for %s from %s (got %s, want %s); keeping the running binary", asset.Name, source, downloaded, expected)
	}

	staged := exe + ".new"
	if err := extractBinary(archive, staged); err != nil {
		_ = os.Remove(staged)
		return err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return err
	}
	if err := replaceBinary(exe, staged); err != nil {
		_ = os.Remove(staged)
		return err
	}
	log.Info("self-update: installed %s over %s", release.TagName, exe)
	return nil
}

// downloadFile streams url into dest, falling back to the mirror prefix when
// the direct request fails. It returns the sha256 of what was written.
func downloadFile(ctx context.Context, client *http.Client, rawURL, mirror, dest string) (sum, source string, err error) {
	sources := []string{rawURL}
	if mirror != "" {
		sources = append(sources, mirrorURL(mirror, rawURL))
	}
	var lastErr error
	for _, candidate := range sources {
		sum, err = fetchToFile(ctx, client, candidate, dest)
		if err == nil {
			return sum, candidate, nil
		}
		lastErr = err
	}
	return "", "", lastErr
}

func fetchToFile(ctx context.Context, client *http.Client, rawURL, dest string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "issuekiller")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, selfMaxAsset+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > selfMaxAsset {
		return "", fmt.Errorf("asset is larger than %d bytes", selfMaxAsset)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func downloadBytes(ctx context.Context, client *http.Client, rawURL, mirror string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "issuekiller")
	response, err := client.Do(request)
	if err != nil && mirror != "" {
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, mirrorURL(mirror, rawURL), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", "issuekiller")
		response, err = client.Do(request)
	}
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

// extractBinary copies the daemon binary out of a release archive. The archive
// is untrusted input: only a regular file with the expected name is accepted.
func extractBinary(archive, dest string) error {
	if strings.HasSuffix(strings.ToLower(archive), ".zip") {
		return extractBinaryZip(archive, dest)
	}
	return extractBinaryTarGz(archive, dest)
}

func isDaemonBinary(name string) bool {
	base := filepath.Base(name)
	return base == "issuekiller" || base == "issuekiller.exe"
}

func extractBinaryTarGz(archive, dest string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", archive, err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", archive, err)
		}
		if header.Typeflag != tar.TypeReg || !isDaemonBinary(header.Name) {
			continue
		}
		return writeReader(dest, reader)
	}
	return fmt.Errorf("%s does not contain issuekiller", archive)
}

func extractBinaryZip(archive, dest string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("read %s: %w", archive, err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if !file.FileInfo().Mode().IsRegular() || !isDaemonBinary(file.Name) {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			return err
		}
		defer opened.Close()
		return writeReader(dest, opened)
	}
	return fmt.Errorf("%s does not contain issuekiller", archive)
}

func writeReader(dest string, source io.Reader) error {
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, io.LimitReader(source, selfMaxAsset)); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// replaceBinary swaps the new binary in. Windows cannot rename over a running
// executable, so the old one is moved aside first and removed on the next best
// effort; POSIX only needs the rename.
func replaceBinary(exe, staged string) error {
	if canExecInPlace {
		if err := os.Rename(staged, exe); err != nil {
			return fmt.Errorf("replace %s: %w", exe, err)
		}
		return nil
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move %s aside: %w", exe, err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	_ = os.Remove(old)
	return nil
}

const (
	masterRepoURL = "https://github.com/uiautodev/issuekiller.git"
	masterBranch  = "master"
	// masterCheck is fast on purpose: on a development machine a push to master
	// should reach the running daemon in under a minute, not half an hour.
	masterCheck = 30 * time.Second
	// masterSettle waits for a burst of pushes to finish so the rebuild targets
	// the final head instead of an intermediate commit.
	masterSettle = 5 * time.Second
)

// watchMaster rebuilds the running binary from master whenever it moves. It is
// the development counterpart of watchSelfUpdates and needs git, go and npm on
// the machine, because the dashboard is compiled from frontend/ into the binary.
// The returned channel fires once after a successful rebuild; the caller waits
// for idle jobs and re-execs.
// masterWatchBaseline is the commit the watcher compares against. A queued
// restart can re-exec a binary that was built before master moved again (the
// restart waits for running jobs); seeding the watch from the remote head in
// that case would treat the newer master as the baseline and never rebuild.
// The injected build commit is authoritative when present.
func masterWatchBaseline(head, buildCommit string) string {
	if buildCommit == "" {
		return head
	}
	return buildCommit
}

func watchMaster(ctx context.Context, log *Logger, env []string) <-chan struct{} {
	ready := make(chan struct{}, 1)
	go func() {
		head := remoteMasterHead(ctx, env)
		if head == "" {
			log.Warn("master update: cannot read %s from %s; disabled", masterBranch, masterRepoURL)
			return
		}
		last := masterWatchBaseline(head, commit)
		log.Debug("master update: watching %s at %s", masterBranch, shortSHA(head))
		ticker := time.NewTicker(masterCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			next := remoteMasterHead(ctx, env)
			if next == "" || next == last {
				continue
			}
			time.Sleep(masterSettle)
			if settled := remoteMasterHead(ctx, env); settled != "" {
				next = settled
			}
			log.Info("master update: new %s commit %s; rebuilding", masterBranch, shortSHA(next))
			if err := rebuildFromMaster(ctx, log, env, next); err != nil {
				log.Error("master update: %v (keeping the running binary)", err)
				last = next
				continue
			}
			last = next
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
	}()
	return ready
}

func remoteMasterHead(ctx context.Context, env []string) string {
	git, err := gitBinary()
	if err != nil {
		return ""
	}
	result, err := runCmd(ctx, git, []string{"ls-remote", masterRepoURL, "refs/heads/" + masterBranch}, CmdOptions{
		Env:          env,
		Timeout:      30 * time.Second,
		AllowFailure: true,
	})
	if err != nil || result.Code != 0 {
		return ""
	}
	return headFromLSRemote(result.Stdout)
}

func headFromLSRemote(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// rebuildFromMaster builds the latest master in a dedicated clone and replaces
// the running executable. The build goes to a temporary file first, so a failed
// build never replaces a working binary.
func rebuildFromMaster(ctx context.Context, log *Logger, env []string, head string) error {
	git, err := gitBinary()
	if err != nil {
		return err
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH: %w", err)
	}
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in PATH: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	dir := filepath.Join(stateDir(), "self")
	if !fileExists(filepath.Join(dir, "go.mod")) {
		_ = os.RemoveAll(dir)
		log.Info("master update: cloning %s into %s", masterRepoURL, dir)
		if _, err := runCmd(ctx, git, []string{"clone", "--depth", "1", "--branch", masterBranch, masterRepoURL, dir}, CmdOptions{
			Env:     env,
			Timeout: 10 * time.Minute,
		}); err != nil {
			return fmt.Errorf("clone: %w", err)
		}
	}
	if _, err := runCmd(ctx, git, []string{"-C", dir, "fetch", "--depth", "1", "origin",
		"+refs/heads/" + masterBranch + ":refs/remotes/origin/" + masterBranch}, CmdOptions{
		Env:     env,
		Timeout: 5 * time.Minute,
	}); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := runCmd(ctx, git, []string{"-C", dir, "reset", "--hard", "origin/" + masterBranch}, CmdOptions{
		Env:     env,
		Timeout: time.Minute,
	}); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}
	if err := buildDashboard(ctx, log, npmBin, dir, env); err != nil {
		return err
	}

	tmp := exe + ".new"
	if _, err := runCmd(ctx, goBin, []string{"build", "-trimpath",
		"-ldflags", "-X main.commit=" + head,
		"-o", tmp, "./cmd/issuekiller"}, CmdOptions{
		Cwd:     dir,
		Env:     env,
		Timeout: 10 * time.Minute,
	}); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("go build: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	log.Info("master update: rebuilt %s at %s", exe, shortSHA(head))
	return nil
}

// buildDashboard compiles frontend/ into cmd/issuekiller/web/dist, which the
// binary embeds. `npm ci` is skipped when the lockfile did not change.
func buildDashboard(ctx context.Context, log *Logger, npmBin, dir string, env []string) error {
	frontend := filepath.Join(dir, "frontend")
	lock, err := os.ReadFile(filepath.Join(frontend, "package-lock.json"))
	if err != nil {
		return nil // dashboard build not part of this tree
	}
	sum := sha256.Sum256(lock)
	stamp := hex.EncodeToString(sum[:])
	stampFile := filepath.Join(frontend, "node_modules", ".issuekiller-lock")
	if current, readErr := os.ReadFile(stampFile); readErr != nil || string(current) != stamp {
		log.Info("master update: installing dashboard dependencies")
		if _, err := runCmd(ctx, npmBin, []string{"ci", "--no-audit", "--no-fund"}, CmdOptions{
			Cwd:     frontend,
			Env:     env,
			Timeout: 10 * time.Minute,
		}); err != nil {
			return fmt.Errorf("npm ci: %w", err)
		}
		if err := os.WriteFile(stampFile, []byte(stamp), 0o644); err != nil {
			return nil
		}
	}
	if _, err := runCmd(ctx, npmBin, []string{"run", "build"}, CmdOptions{
		Cwd:     frontend,
		Env:     env,
		Timeout: 10 * time.Minute,
	}); err != nil {
		return fmt.Errorf("npm run build: %w", err)
	}
	return nil
}

// watchSelfUpdates polls releases/latest and installs a newer one. The returned
// channel fires once, after a successful install, so the caller can restart
// when the running jobs are idle. Local "dev" builds are never replaced
// automatically; the dashboard can still install a release on demand.
func watchSelfUpdates(ctx context.Context, log *Logger, cfg *Config) <-chan struct{} {
	ready := make(chan struct{}, 1)
	go func() {
		if _, _, ok := parseVersion(version); !ok {
			log.Info("self-update: local build %q; automatic release updates are off (install one from the dashboard)", version)
			return
		}
		check := func() bool {
			release, err := lookupLatestRelease(ctx, cfg.Proxy)
			if err != nil {
				log.Warn("self-update: %v", err)
				return false
			}
			if !isNewerVersion(release.TagName, version) {
				log.Debug("self-update: %s is the latest release", version)
				return false
			}
			log.Info("self-update: %s is available (running %s); downloading", release.TagName, version)
			if err := installRelease(ctx, log, release, cfg.Proxy, cfg.UpdateProxy); err != nil {
				log.Error("self-update: %v (keeping the running binary)", err)
				return false
			}
			return true
		}
		if check() {
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
		ticker := time.NewTicker(selfCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if check() {
				select {
				case ready <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	return ready
}

// mirrorURL joins a mirror prefix and an absolute URL. gh-proxy style mirrors
// take the whole URL after the prefix.
func mirrorURL(prefix, rawURL string) string {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" || rawURL == "" {
		return rawURL
	}
	return prefix + "/" + rawURL
}

// normalizeMirror accepts gh-proxy.com, https://gh-proxy.com/ or an empty
// string and returns the canonical prefix (empty means "direct only").
func normalizeMirror(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch strings.ToLower(value) {
	case "", "none", "direct", "off", "-":
		return "", nil
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid updateProxy %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("updateProxy must be an http(s) URL, got %q", raw)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("updateProxy must include a host: %q", raw)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
