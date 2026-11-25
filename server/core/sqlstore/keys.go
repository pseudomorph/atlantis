// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
)

var _ db.LockKeyCodec = (*Store)(nil)

type lockKey struct {
	Instance                               models.StorageInstance
	Provider                               models.VCSHostType
	Repository, Directory, Name, Workspace string
}

func (s *Store) LockKey(repo models.Repo, p models.Project, workspace string) string {
	key := lockKey{repo.StorageIdentity(), repo.VCSHost.Type, repo.FullName, p.Path, p.ProjectName, workspace}
	data, _ := json.Marshal(key)
	return "sql/" + base64.RawURLEncoding.EncodeToString(data)
}

func (s *Store) ParseLockKey(value string) (models.Repo, models.Project, string, error) {
	var key lockKey
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "sql/"))
	if !strings.HasPrefix(value, "sql/") || err != nil || json.Unmarshal(data, &key) != nil || key.Repository == "" {
		return models.Repo{}, models.Project{}, "", fmt.Errorf("invalid durable-storage lock key")
	}
	// Reconstruct only identity fields. Credentials never appear in lock keys.
	authority := key.Instance.Host
	if key.Instance.Port != "" {
		authority = net.JoinHostPort(authority, key.Instance.Port)
	} else if strings.Contains(authority, ":") {
		authority = "[" + authority + "]"
	}
	path := key.Instance.BasePath + "/" + key.Repository
	if key.Provider == models.BitbucketServer {
		path = key.Instance.BasePath + "/scm/" + key.Repository
	}
	if key.Provider == models.AzureDevops {
		if i := strings.LastIndex(key.Repository, "/"); i >= 0 {
			path = key.Instance.BasePath + "/" + key.Repository[:i] + "/_git/" + key.Repository[i+1:]
		}
	}
	u := url.URL{Scheme: "storage", Host: authority, Path: path + ".git"}
	repo := models.Repo{FullName: key.Repository, CloneURL: u.String(), VCSHost: models.VCSHost{Hostname: key.Instance.Host, Type: key.Provider}}
	return repo, models.NewProject(key.Repository, key.Directory, key.Name), key.Workspace, nil
}
