// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestIsUnsafeNonPRRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
		want bool
	}{
		{name: "GitHub pull head", ref: "pull/123/head", want: true},
		{name: "GitHub refs pull merge", ref: "refs/pull/123/merge", want: true},
		{name: "GitHub plus refs pull head", ref: "+refs/pull/123/head", want: true},
		{name: "GitLab merge request head", ref: "merge-requests/123/head", want: true},
		{name: "GitLab refs merge request merge", ref: "refs/merge-requests/123/merge", want: true},
		{name: "GitLab plus refs merge request merge", ref: "+refs/merge-requests/123/merge", want: true},
		{name: "GitHub pull refspec", ref: "refs/pull/123/head:refs/tmp/x", want: true},
		{name: "GitHub short pull refspec", ref: "pull/123/head:refs/tmp/x", want: true},
		{name: "GitLab merge request refspec", ref: "refs/merge-requests/123/head:refs/tmp/x", want: true},
		{name: "branch", ref: "main", want: false},
		{name: "tag", ref: "refs/tags/v1.2.3", want: false},
		{name: "sha", ref: "0123456789abcdef0123456789abcdef01234567", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := models.IsUnsafeAPIRef(tc.ref); got != tc.want {
				t.Fatalf("models.IsUnsafeAPIRef(%q) = %t, want %t", tc.ref, got, tc.want)
			}
		})
	}
}

func TestDurableWorkingDirectoriesAreInstanceScoped(t *testing.T) {
	w := &FileWorkspace{DataDir: t.TempDir(), InstanceScoped: true}
	repo := models.Repo{FullName: "org/repo", CloneURL: "https://git.example:8443/gitlab/org/repo.git", VCSHost: models.VCSHost{Type: models.Gitlab}}
	a, err := w.repoPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	repo.CloneURL = "https://git.example/other/org/repo.git"
	b, err := w.repoPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	Assert(t, a != b, "instances share a working directory")
	planDir, err := w.planPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	Equals(t, b, planDir)
}
