package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Periodic release publishing. A repository opts in with release.enabled; the
// daemon then checks the default branch at most once per intervalDays and
// publishes a GitHub release when it moved past the newest v* tag, or v0.1.0
// when the repository has no version tag yet. Everything here is deliberately
// small: the tag is the only version source, and the conventional-commit
// subject decides whether the batch is a feature.

const (
	// releaseTick re-evaluates each repo's schedule; the config interval decides
	// whether that tick may call GitHub. Hourly keeps the daemon's clock coarse
	// without waiting for the exact interval to elapse before trying again.
	releaseTick = time.Hour
	// releaseCommitLimit caps the first release of a repository, which has no
	// previous tag to compare against.
	releaseCommitLimit = 100
)

// releaseVersion is the MAJOR.MINOR.PATCH triple of a release tag.
type releaseVersion struct {
	Major int
	Minor int
	Patch int
}

// versionFromTag parses vMAJOR.MINOR.PATCH. The existing self-update parser is
// reused so both features read release tags the same way.
func versionFromTag(tag string) (releaseVersion, bool) {
	parts, _, ok := parseVersion(tag)
	if !ok {
		return releaseVersion{}, false
	}
	return releaseVersion{Major: parts[0], Minor: parts[1], Patch: parts[2]}, true
}

func (v releaseVersion) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// latestVersionTag returns the highest v* tag. Non-version tags are ignored so
// a repository's own labels cannot disturb the sequence. A repository without
// any version tag returns ok=false; checkRelease then publishes initialRelease.
func latestVersionTag(tags []Tag) (releaseVersion, string, bool) {
	best := ""
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if _, ok := versionFromTag(name); !ok {
			continue
		}
		if best == "" || compareVersions(name, best) > 0 {
			best = name
		}
	}
	if best == "" {
		return releaseVersion{}, "", false
	}
	version, _ := versionFromTag(best)
	return version, best, true
}

const (
	bumpPatch = "patch"
	bumpMinor = "minor"
	bumpMajor = "major"
)

// bumpVersion increments one part and resets the parts to its right, so a minor
// release clears the patch and a major release clears both.
func bumpVersion(version releaseVersion, part string) releaseVersion {
	switch part {
	case bumpMajor:
		return releaseVersion{Major: version.Major + 1}
	case bumpMinor:
		return releaseVersion{Major: version.Major, Minor: version.Minor + 1}
	default:
		return releaseVersion{Major: version.Major, Minor: version.Minor, Patch: version.Patch + 1}
	}
}

const (
	commitFeature = "feature"
	commitFix     = "fix"
)

// classifyCommit reads a conventional-commit subject: feat/feature is a
// feature, everything else is a fix. Squash-merged PR titles follow the same
// convention, which is why the prefix and not a PR label is the signal.
func classifyCommit(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	kind := lower
	if index := strings.IndexAny(lower, "(!:"); index >= 0 {
		kind = lower[:index]
	}
	if kind == "feat" || kind == "feature" {
		return commitFeature
	}
	return commitFix
}

// initialRelease is the first version of a repository that has no version tag
// yet. It is always v0.1.0: a bump describes a change relative to a published
// version, and a first v0.0.1 would read as a patch to a release that never
// happened, whatever the commit mix contains.
var initialRelease = releaseVersion{Minor: 1}

// nextReleaseVersion applies the conventional-commit rule: a feature commit
// bumps the minor part, any other commit the patch part. A major release is a
// deliberate human decision, so the checker never publishes one by itself.
func nextReleaseVersion(current releaseVersion, titles []string) releaseVersion {
	part := bumpPatch
	for _, title := range titles {
		if classifyCommit(title) == commitFeature {
			part = bumpMinor
			break
		}
	}
	return bumpVersion(current, part)
}

// releaseDue reports whether the last check is at least intervalDays old. An
// empty or unparsable timestamp means "never checked", so a fresh state file
// and an upgraded one both check on the next tick.
func releaseDue(lastCheck string, intervalDays int, now time.Time) bool {
	if strings.TrimSpace(lastCheck) == "" {
		return true
	}
	checked, err := time.Parse(time.RFC3339, lastCheck)
	if err != nil {
		return true
	}
	if intervalDays < 1 {
		intervalDays = 1
	}
	return !now.Before(checked.Add(time.Duration(intervalDays) * 24 * time.Hour))
}

func commitTitle(commit Commit) string {
	message := strings.TrimSpace(commit.Commit.Message)
	if line, _, ok := strings.Cut(message, "\n"); ok {
		return strings.TrimSpace(line)
	}
	return message
}

// releaseNotes lists the commits of this batch. Commit subjects are kept
// verbatim so the release follows the repository's own commit language; a
// release has no issue or PR to detect a scaffolding language from, so the
// compare footer is the only fixed line and matches GitHub's own format.
func releaseNotes(fullName, previous string, version releaseVersion, commits []Commit) string {
	var builder strings.Builder
	for _, commit := range commits {
		title := commitTitle(commit)
		if title == "" {
			continue
		}
		if sha := shortSHA(commit.SHA); sha != "" {
			fmt.Fprintf(&builder, "- %s (%s)\n", title, sha)
		} else {
			fmt.Fprintf(&builder, "- %s\n", title)
		}
	}
	if previous != "" {
		fmt.Fprintf(&builder, "\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n", fullName, previous, version)
	}
	return strings.TrimRight(builder.String(), "\n")
}

// checkRelease publishes at most one release per call. A repo that is not due,
// has no new commits, or fails a request publishes nothing; only a successful
// check (including "no commits") moves LastReleaseCheckAt, so a failed attempt
// retries on the next tick instead of sleeping for a whole interval.
func checkRelease(ctx context.Context, app *AppContext) error {
	cfg := app.Repo.Release
	if !cfg.Enabled {
		return nil
	}
	if app.GH == nil || app.Store == nil || app.Log == nil {
		return fmt.Errorf("release check needs a GitHub client, state store and logger")
	}
	base := app.RepoInfo.DefaultBranch
	if app.Local != nil && app.Local.Base != "" {
		base = app.Local.Base
	}
	if base == "" {
		return fmt.Errorf("release check needs the default branch")
	}
	if !releaseDue(app.Store.Meta(app.Repo.FullName).LastReleaseCheckAt, cfg.IntervalDays, time.Now()) {
		app.Log.Debug("%s: release check not due yet", app.Repo.FullName)
		return nil
	}

	tags, err := app.GH.ListTags(ctx, app.Repo.FullName)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	current, previous, tagged := latestVersionTag(tags)
	var commits []Commit
	if tagged {
		commits, err = app.GH.CompareCommits(ctx, app.Repo.FullName, previous, base)
	} else {
		commits, err = app.GH.ListCommits(ctx, app.Repo.FullName, base, releaseCommitLimit)
	}
	if err != nil {
		return fmt.Errorf("list commits on %s: %w", base, err)
	}
	if len(commits) == 0 {
		app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
			meta.LastReleaseCheckAt = nowISO()
		})
		app.Log.Debug("%s: no commits since %s, nothing to release", app.Repo.FullName, firstNonEmpty(previous, "the first commit"))
		return nil
	}

	titles := make([]string, 0, len(commits))
	for _, commit := range commits {
		titles = append(titles, commitTitle(commit))
	}
	next := initialRelease
	if tagged {
		next = nextReleaseVersion(current, titles)
	}
	notes := releaseNotes(app.Repo.FullName, previous, next, commits)
	if _, err := app.GH.CreateRelease(ctx, app.Repo.FullName, next.String(), next.String(), notes, base); err != nil {
		return fmt.Errorf("create release %s: %w", next, err)
	}
	app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
		meta.LastReleaseCheckAt = nowISO()
	})
	app.Log.Info("%s: published release %s (%d commits since %s)", app.Repo.FullName, next, len(commits), firstNonEmpty(previous, "the first commit"))
	return nil
}

// startReleaseChecker runs one loop per opted-in repository. The repo is
// checked at startup and then every releaseTick; each check is independent, so
// a slow GitHub call in one repo does not delay the others and nothing touches
// the poller or the job queues.
func startReleaseChecker(ctx context.Context, contexts map[string]*AppContext, log *Logger) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	enabled := 0
	for fullName, app := range contexts {
		if app == nil || !app.Repo.Release.Enabled {
			continue
		}
		enabled++
		wg.Add(1)
		go func(name string, app *AppContext) {
			defer wg.Done()
			check := func() {
				if err := checkRelease(ctx, app); err != nil {
					log.Warn("%s: release check: %v", name, err)
				}
			}
			check()
			ticker := time.NewTicker(releaseTick)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					check()
				}
			}
		}(fullName, app)
	}
	if enabled == 0 {
		log.Debug("release checks disabled (no repo sets release.enabled)")
	} else {
		log.Info("release checks started for %d repo(s), tick %s", enabled, releaseTick)
	}
	return func() {
		close(stop)
		wg.Wait()
	}
}
