// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"net"
	"net/url"
	"strings"
)

// StorageInstance identifies a VCS deployment independently of a repository.
type StorageInstance struct {
	Host     string
	Port     string
	BasePath string
}

// StorageIdentity retains the authority and installation path discarded by Hostname.
func (r Repo) StorageIdentity() StorageInstance {
	instance := StorageInstance{Host: strings.ToLower(r.VCSHost.Hostname)}
	u, err := url.Parse(r.CloneURL)
	if err != nil || u.Host == "" {
		return instance
	}
	instance.Host = strings.ToLower(u.Hostname())
	instance.Port = u.Port()
	if (u.Scheme == "https" && instance.Port == "443") || (u.Scheme == "http" && instance.Port == "80") {
		instance.Port = ""
	}
	clonePath := strings.TrimSuffix(u.Path, ".git")
	suffix := "/" + r.FullName
	switch r.VCSHost.Type {
	case BitbucketServer:
		suffix = "/scm/" + r.FullName
	case AzureDevops:
		if i := strings.LastIndex(r.FullName, "/"); i >= 0 {
			suffix = "/" + r.FullName[:i] + "/_git/" + r.FullName[i+1:]
		}
	}
	if strings.HasSuffix(strings.ToLower(clonePath), strings.ToLower(suffix)) {
		instance.BasePath = strings.TrimSuffix(clonePath[:len(clonePath)-len(suffix)], "/")
	}
	return instance
}

// Directory returns one safe filesystem component for this instance.
func (i StorageInstance) Directory() string {
	name := i.Host
	if i.Port != "" {
		name = net.JoinHostPort(i.Host, i.Port)
	}
	name += i.BasePath
	if name == "" {
		return "_"
	}
	if name == "." || name == ".." {
		return strings.ReplaceAll(name, ".", "%2E")
	}
	return url.PathEscape(name)
}
