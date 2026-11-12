// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/go-github/v88/github"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// memoryDeliveries records deliveries in memory, as durable storage does in its database.
type memoryDeliveries struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func (m *memoryDeliveries) RecordDelivery(provider, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	key := provider + "/" + id
	if m.seen[key] {
		return false, nil
	}
	m.seen[key] = true
	return true, nil
}

func (m *memoryDeliveries) ForgetDelivery(provider, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.seen, provider+"/"+id)
	return nil
}

func githubComment(delivery string) *http.Request {
	req, _ := http.NewRequest("POST", "", bytes.NewBuffer(nil))
	req.Header.Set(githubHeader, "issue_comment")
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	return req
}

func TestPost_DuplicateGithubDeliveryHasNoSideEffects(t *testing.T) {
	e, v, _, _, p, cr, _, _, cp := setup(t)
	deliveries := &memoryDeliveries{}
	e.Deliveries = deliveries
	When(v.Validate(Any[*http.Request](), Eq(secret))).ThenReturn([]byte(`{"action": "created"}`), nil)
	cmd := events.CommentCommand{}
	When(p.ParseGithubIssueCommentEvent(Any[logging.SimpleLogging](), Any[*github.IssueCommentEvent]())).ThenReturn(models.Repo{}, models.User{}, 1, nil)
	When(cp.Parse("", models.Github)).ThenReturn(events.CommentParseResult{Command: &cmd})

	w := httptest.NewRecorder()
	e.Post(w, githubComment("delivery-1"))
	ResponseContains(t, w, http.StatusOK, "Processing...")
	w = httptest.NewRecorder()
	e.Post(w, githubComment("delivery-1"))
	ResponseContains(t, w, http.StatusOK, "Ignoring duplicate webhook delivery delivery-1")
	cr.VerifyWasCalledOnce().RunCommentCommand(models.Repo{}, nil, nil, models.User{}, 1, &cmd)

	// Another delivery of an identical payload is a new event.
	w = httptest.NewRecorder()
	e.Post(w, githubComment("delivery-2"))
	ResponseContains(t, w, http.StatusOK, "Processing...")
	cr.VerifyWasCalled(Times(2)).RunCommentCommand(models.Repo{}, nil, nil, models.User{}, 1, &cmd)
}

// Without durable storage, and without a delivery ID, every request is handled.
func TestPost_DeliveriesHandledWithoutDeduplication(t *testing.T) {
	for _, c := range []struct {
		name       string
		deliveries bool
		delivery   string
	}{{"legacy storage", false, "delivery-1"}, {"no delivery ID", true, ""}} {
		t.Run(c.name, func(t *testing.T) {
			e, v, _, _, p, cr, _, _, cp := setup(t)
			if c.deliveries {
				e.Deliveries = &memoryDeliveries{}
			}
			When(v.Validate(Any[*http.Request](), Eq(secret))).ThenReturn([]byte(`{"action": "created"}`), nil)
			cmd := events.CommentCommand{}
			When(p.ParseGithubIssueCommentEvent(Any[logging.SimpleLogging](), Any[*github.IssueCommentEvent]())).ThenReturn(models.Repo{}, models.User{}, 1, nil)
			When(cp.Parse("", models.Github)).ThenReturn(events.CommentParseResult{Command: &cmd})
			for range 2 {
				w := httptest.NewRecorder()
				e.Post(w, githubComment(c.delivery))
				ResponseContains(t, w, http.StatusOK, "Processing...")
			}
			cr.VerifyWasCalled(Times(2)).RunCommentCommand(models.Repo{}, nil, nil, models.User{}, 1, &cmd)
		})
	}
}

// A delivery that fails is forgotten, so the provider's retry is handled. A
// request that fails authentication is never recorded.
func TestPost_FailedDeliveryCanBeRetried(t *testing.T) {
	e, v, _, _, p, _, _, _, _ := setup(t)
	deliveries := &memoryDeliveries{}
	e.Deliveries = deliveries
	When(v.Validate(Any[*http.Request](), Eq(secret))).ThenReturn([]byte(`{"action": "created"}`), nil)
	When(p.ParseGithubIssueCommentEvent(Any[logging.SimpleLogging](), Any[*github.IssueCommentEvent]())).ThenReturn(models.Repo{}, models.User{}, 1, errors.New("bad payload"))
	w := httptest.NewRecorder()
	e.Post(w, githubComment("delivery-1"))
	Equals(t, http.StatusBadRequest, w.Code)
	Equals(t, 0, len(deliveries.seen))

	e, v, _, _, _, _, _, _, _ = setup(t)
	e.Deliveries = deliveries
	When(v.Validate(Any[*http.Request](), Eq(secret))).ThenReturn(nil, errors.New("invalid signature"))
	w = httptest.NewRecorder()
	e.Post(w, githubComment("delivery-2"))
	Equals(t, http.StatusBadRequest, w.Code)
	Equals(t, 0, len(deliveries.seen))
}

// A delivery that cannot be recorded is not handled, and the provider retries it.
func TestPost_UnrecordedDeliveryIsRefused(t *testing.T) {
	e, v, _, _, _, cr, _, _, _ := setup(t)
	e.Deliveries = &memoryDeliveries{err: errors.New("database unavailable")}
	When(v.Validate(Any[*http.Request](), Eq(secret))).ThenReturn([]byte(`{"action": "created"}`), nil)
	w := httptest.NewRecorder()
	e.Post(w, githubComment("delivery-1"))
	ResponseContains(t, w, http.StatusServiceUnavailable, "database unavailable")
	cr.VerifyWasCalled(Never()).RunCommentCommand(Any[models.Repo](), Any[*models.Repo](), Any[*models.PullRequest](), Any[models.User](), Any[int](), Any[*events.CommentCommand]())
}

// Each provider's delivery ID header identifies redeliveries.
func TestPost_DeliveryHeaderPerProvider(t *testing.T) {
	for _, c := range []struct {
		name    string
		headers map[string]string
	}{
		{"gitlab", map[string]string{gitlabHeader: "Note Hook", "X-Gitlab-Event-UUID": "event-1"}},
		{"gitea", map[string]string{giteaHeader: "unknown", "X-Gitea-Delivery": "event-1"}},
		{"bitbucket cloud", map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": "event-1"}},
		{"bitbucket server", map[string]string{"X-Event-Key": "repo:refs_changed", "X-Request-ID": "event-1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, gl, _, _, _, _, _, _ := setup(t)
			e.Deliveries = &memoryDeliveries{}
			e.GiteaWebhookSecret = nil
			e.SupportedVCSHosts = append(e.SupportedVCSHosts, models.BitbucketCloud, models.BitbucketServer)
			When(gl.ParseAndValidate(Any[*http.Request](), Eq(secret))).ThenReturn([]byte(`{"not an event": ""}`), nil)
			request := func() *http.Request {
				req, _ := http.NewRequest("POST", "", bytes.NewBuffer(nil))
				for name, value := range c.headers {
					req.Header.Set(name, value)
				}
				return req
			}
			w := httptest.NewRecorder()
			e.Post(w, request())
			ResponseContains(t, w, http.StatusOK, "Ignoring unsupported")
			w = httptest.NewRecorder()
			e.Post(w, request())
			ResponseContains(t, w, http.StatusOK, "Ignoring duplicate webhook delivery event-1")
		})
	}
}
