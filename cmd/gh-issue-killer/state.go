package main

import (
	"sync"
)

type GlobalState struct {
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

type IssueMeta struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Author string   `json:"author"`
	URL    string   `json:"url"`
	Labels []string `json:"labels"`
}

type CheckResult struct {
	Name        string `json:"name"`
	Cmd         string `json:"cmd"`
	Cwd         string `json:"cwd"`
	OK          bool   `json:"ok"`
	Code        int    `json:"code"`
	TimedOut    bool   `json:"timedOut,omitempty"`
	DurationSec int    `json:"durationSec,omitempty"`
	OutputTail  string `json:"outputTail,omitempty"`
}

type Shot struct {
	Name    string `json:"name"`
	Caption string `json:"caption"`
	URL     string `json:"url"`
	Size    int64  `json:"size,omitempty"`
}

type IssueState struct {
	Phase             string        `json:"phase"`
	Plan              string        `json:"plan,omitempty"`
	PlanCommentID     int64         `json:"planCommentId,omitempty"`
	SessionID         string        `json:"sessionId,omitempty"`
	LastCommentID     int64         `json:"lastCommentId,omitempty"`
	ProcessedComments []int64       `json:"processedComments,omitempty"`
	Branch            string        `json:"branch,omitempty"`
	Worktree          string        `json:"worktree,omitempty"`
	PRNumber          int           `json:"prNumber,omitempty"`
	PRURL             string        `json:"prUrl,omitempty"`
	PRCommentID       int64         `json:"prCommentId,omitempty"`
	Checks            []CheckResult `json:"checks,omitempty"`
	Screenshots       []Shot        `json:"screenshots,omitempty"`
	BlockedReason     string        `json:"blockedReason,omitempty"`
	Issue             *IssueMeta    `json:"issue,omitempty"`
	CreatedAt         string        `json:"createdAt,omitempty"`
	UpdatedAt         string        `json:"updatedAt,omitempty"`
	StartedAt         string        `json:"startedAt,omitempty"`
	MergedAt          string        `json:"mergedAt,omitempty"`
	CancelledAt       string        `json:"cancelledAt,omitempty"`
	IssueClosed       bool          `json:"issueClosed,omitempty"`
}

type RepoMeta struct {
	LastPollAt          string   `json:"lastPollAt,omitempty"`
	ProcessedDeliveries []string `json:"processedDeliveries,omitempty"`
	SmeeChannel         string   `json:"smeeChannel,omitempty"`
	HookID              int64    `json:"hookId,omitempty"`
	WebhookSecret       string   `json:"webhookSecret,omitempty"`
}

type RepoState struct {
	Issues map[string]*IssueState `json:"issues"`
	Meta   RepoMeta               `json:"meta"`
}

type State struct {
	Version int                   `json:"version"`
	Global  GlobalState           `json:"global"`
	Repos   map[string]*RepoState `json:"repos"`
}

type Store struct {
	mu    sync.Mutex
	state State
	file  string
}

func LoadStore() (*Store, error) {
	store := &Store{file: stateFile()}
	store.state = State{Version: 1, Repos: map[string]*RepoState{}}
	_ = readJSONIfExists(store.file, &store.state)
	if store.state.Repos == nil {
		store.state.Repos = map[string]*RepoState{}
	}
	return store, nil
}

func (s *Store) SaveLocked() error {
	return atomicWriteJSON(s.file, s.state)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SaveLocked()
}

func (s *Store) repoLocked(fullName string) *RepoState {
	repo := s.state.Repos[fullName]
	if repo == nil {
		repo = &RepoState{Issues: map[string]*IssueState{}}
		s.state.Repos[fullName] = repo
	}
	if repo.Issues == nil {
		repo.Issues = map[string]*IssueState{}
	}
	return repo
}

func (s *Store) Meta(fullName string) RepoMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repoLocked(fullName).Meta
}

func (s *Store) UpdateMeta(fullName string, fn func(*RepoMeta)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo := s.repoLocked(fullName)
	fn(&repo.Meta)
	_ = s.SaveLocked()
}

func (s *Store) Issue(fullName string, number int) IssueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo := s.repoLocked(fullName)
	if issue := repo.Issues[itoa(number)]; issue != nil {
		return *issue
	}
	return IssueState{}
}

func (s *Store) UpdateIssue(fullName string, number int, fn func(*IssueState)) IssueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo := s.repoLocked(fullName)
	key := itoa(number)
	issue := repo.Issues[key]
	if issue == nil {
		issue = &IssueState{Phase: "new", CreatedAt: nowISO()}
		repo.Issues[key] = issue
	}
	fn(issue)
	issue.UpdatedAt = nowISO()
	_ = s.SaveLocked()
	return *issue
}

func (s *Store) IsCommentProcessed(fullName string, number int, commentID int64) bool {
	issue := s.Issue(fullName, number)
	for _, id := range issue.ProcessedComments {
		if id == commentID {
			return true
		}
	}
	return false
}

func (s *Store) MarkCommentProcessed(fullName string, number int, commentID int64) {
	s.UpdateIssue(fullName, number, func(issue *IssueState) {
		for _, id := range issue.ProcessedComments {
			if id == commentID {
				issue.LastCommentID = commentID
				return
			}
		}
		issue.ProcessedComments = append(issue.ProcessedComments, commentID)
		if len(issue.ProcessedComments) > 500 {
			issue.ProcessedComments = issue.ProcessedComments[len(issue.ProcessedComments)-500:]
		}
		issue.LastCommentID = commentID
	})
}

func (s *Store) IsDeliveryProcessed(fullName, delivery string) bool {
	if delivery == "" {
		return false
	}
	meta := s.Meta(fullName)
	for _, id := range meta.ProcessedDeliveries {
		if id == delivery {
			return true
		}
	}
	return false
}

func (s *Store) MarkDeliveryProcessed(fullName, delivery string) {
	if delivery == "" {
		return
	}
	s.UpdateMeta(fullName, func(meta *RepoMeta) {
		for _, id := range meta.ProcessedDeliveries {
			if id == delivery {
				return
			}
		}
		meta.ProcessedDeliveries = append(meta.ProcessedDeliveries, delivery)
		if len(meta.ProcessedDeliveries) > 1000 {
			meta.ProcessedDeliveries = meta.ProcessedDeliveries[len(meta.ProcessedDeliveries)-1000:]
		}
	})
}

func (s *Store) GlobalSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Global.WebhookSecret
}

func (s *Store) SetGlobalSecret(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Global.WebhookSecret = secret
	_ = s.SaveLocked()
}
