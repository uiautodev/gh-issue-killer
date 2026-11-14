package main

import "testing"

func TestCanPushToPR(t *testing.T) {
	app := &AppContext{
		Repo:  RepoConfig{FullName: "acme/app"},
		Local: &LocalRepo{HTTPSURL: "https://github.com/acme/app.git"},
	}

	sameRepo := PullRequest{}
	sameRepo.Head.Repo.FullName = "acme/app"
	sameRepo.Head.Ref = "feature"
	if !canPushToPR(app, sameRepo) {
		t.Fatal("same-repo branch should be pushable")
	}
	if got := prPushURL(app, sameRepo); got != app.Local.HTTPSURL {
		t.Fatalf("push url = %q, want the base repository", got)
	}

	forkAllowed := PullRequest{MaintainerCanModify: true}
	forkAllowed.Head.Repo.FullName = "contributor/app"
	forkAllowed.Head.Ref = "fix"
	if !canPushToPR(app, forkAllowed) {
		t.Fatal("fork with allow-edits-by-maintainers should be pushable")
	}
	if got := prPushURL(app, forkAllowed); got != "https://github.com/contributor/app.git" {
		t.Fatalf("push url = %q, want the fork", got)
	}

	forkDenied := PullRequest{}
	forkDenied.Head.Repo.FullName = "contributor/app"
	if canPushToPR(app, forkDenied) {
		t.Fatal("fork without allow-edits-by-maintainers should not be pushable")
	}

	deletedFork := PullRequest{MaintainerCanModify: true}
	if canPushToPR(app, deletedFork) {
		t.Fatal("missing head repository should not be pushable")
	}
}
