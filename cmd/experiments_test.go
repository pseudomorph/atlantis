// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"strings"
	"testing"

	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableStorageExperiments(t *testing.T) {
	for _, tc := range []struct {
		name, config, env string
		args, want        []string
		err               string
	}{
		{name: "default"},
		{name: "unknown storage key", config: "experiments: [durable-storage]\nstorage: {database: {type: sqlite, typoo: x}}", err: "invalid storage configuration"},
		{name: "invalid database", config: "experiments: [durable-storage]\nstorage: {database: {type: redis}}", err: "storage.database.type"},
		{name: "legacy share-plan-dir", config: "experiments: [durable-storage]\nstorage: {database: {type: sqlite}}", args: []string{"--share-plan-dir=/tmp/old-plans"}, err: "storage cannot be combined"},
		{name: "explicit legacy default", config: "experiments: [durable-storage]\nlocking-db-type: boltdb\nstorage: {database: {type: sqlite}}", err: "storage cannot be combined"},

		{name: "yaml", config: "experiments: [durable-storage]\nstorage: {database: {type: sqlite}}", want: []string{"durable-storage"}},
		{name: "environment", env: "durable-storage", want: []string{"durable-storage"}},
		{name: "environment list", env: "durable-storage,durable-storage", want: []string{"durable-storage", "durable-storage"}},
		{name: "coexists with upstream experiments", args: []string{"--experiments=durable-storage,api,drift-detection", "--api-secret=token", "--enable-drift-detection"}, want: []string{"durable-storage", "api", "drift-detection"}},
		{name: "storage does not permit API", args: []string{"--experiments=durable-storage", "--api-secret=token"}, err: "api-secret requires"},
		{name: "storage does not permit drift", args: []string{"--experiments=durable-storage", "--enable-drift-detection"}, err: "enable-drift-detection requires"},
		{name: "flag", args: []string{"--experiments=durable-storage"}, want: []string{"durable-storage"}},
		{name: "repeated", args: []string{"--experiments=durable-storage", "--experiments=durable-storage"}, want: []string{"durable-storage", "durable-storage"}},
		{name: "flag overrides environment and yaml", config: "experiments: [invalid]", env: "invalid", args: []string{"--experiments=durable-storage"}, want: []string{"durable-storage"}},
		{name: "environment overrides yaml", config: "experiments: [invalid]", env: "durable-storage", want: []string{"durable-storage"}},
		{name: "clear flag", config: "experiments: [durable-storage]", args: []string{"--experiments="}},
		{name: "unknown", args: []string{"--experiments=unknown"}, err: "unknown server experiment"},
		{name: "case sensitive", args: []string{"--experiments=Durable-Storage"}, err: "unknown server experiment"},
		{name: "wildcard", args: []string{"--experiments=*"}, err: "unknown server experiment"},
		{name: "missing permission", config: "storage: {database: {type: sqlite}}", err: "requires --experiments=durable-storage"},
		{name: "permission cleared", config: "experiments: [durable-storage]\nstorage: {database: {type: sqlite}}", args: []string{"--experiments="}, err: "requires --experiments=durable-storage"},

		{name: "legacy store conflicts", config: "experiments: [durable-storage]\nstorage: {database: {type: sqlite}}", args: []string{"--enable-external-stores"}, err: "storage cannot be combined"},
		{name: "invalid backend", args: []string{"--locking-db-type=sqltie"}, err: "invalid --locking-db-type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ATLANTIS_EXPERIMENTS", tc.env)
			flags := map[string]any{}
			if tc.config != "" {
				name := tempFile(t, tc.config)
				t.Cleanup(func() { _ = os.Remove(name) })
				flags[ConfigFlag] = name
			}
			cmd := setupWithDefaults(flags, t)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if tc.err != "" {
				Assert(t, err != nil && strings.Contains(err.Error(), tc.err), "error = %v", err)

				return
			}
			Ok(t, err)
			Equals(t, strings.Join(tc.want, ","), strings.Join(passedConfig.Experiments, ","))
			if !strings.Contains(tc.config, "sqlite") && !strings.Contains(strings.Join(tc.args, " "), "sqlite") {
				Equals(t, "boltdb", passedConfig.LockingDBType)
			}
		})
	}
}
