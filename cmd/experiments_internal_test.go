// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"strings"
	"testing"

	. "github.com/runatlantis/atlantis/testing"
)

func TestExecute_Experiments(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		env  string
		args []string
		want []string
		err  string
	}{
		{name: "default"},
		{name: "flag", args: []string{"--experiments=drift-detection"}, want: []string{"drift-detection"}},
		{name: "repeated flags", args: []string{"--experiments=drift-detection", "--experiments=drift-detection"}, want: []string{"drift-detection", "drift-detection"}},
		{name: "comma separated flags", args: []string{"--experiments=drift-detection,drift-detection"}, want: []string{"drift-detection", "drift-detection"}},
		{name: "environment", env: "drift-detection", want: []string{"drift-detection"}},
		{name: "comma separated environment", env: "drift-detection,drift-detection", want: []string{"drift-detection", "drift-detection"}},
		{name: "yaml", yaml: "experiments:\n  - drift-detection\n", want: []string{"drift-detection"}},
		{name: "empty yaml list", yaml: "experiments: []\n"},
		{name: "environment overrides yaml", yaml: "experiments: [unknown]\n", env: "drift-detection", want: []string{"drift-detection"}},
		{name: "flag overrides environment and yaml", yaml: "experiments: [unknown]\n", env: "unknown", args: []string{"--experiments=drift-detection"}, want: []string{"drift-detection"}},
		{name: "empty flag clears lower precedence", yaml: "experiments: [unknown]\n", env: "unknown", args: []string{"--experiments="}},
		{name: "unknown", args: []string{"--experiments=unknown"}, err: `unknown server experiment "unknown"`},
		{name: "unknown environment", env: "unknown", err: `unknown server experiment "unknown"`},
		{name: "unknown yaml", yaml: "experiments: [unknown]\n", err: `unknown server experiment "unknown"`},
		{name: "case sensitive", args: []string{"--experiments=Drift-Detection"}, err: `unknown server experiment "Drift-Detection"`},
		{name: "no wildcard", args: []string{"--experiments=*"}, err: `unknown server experiment "*"`},
		{name: "no all", args: []string{"--experiments=all"}, err: `unknown server experiment "all"`},
		{name: "no whitespace normalization", env: " drift-detection", err: `unknown server experiment " drift-detection"`},
		{name: "no whitespace separated environment", env: "drift-detection drift-detection", err: "unknown server experiment"},
		{name: "empty entry", args: []string{"--experiments=drift-detection,"}, err: `unknown server experiment ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ATLANTIS_EXPERIMENTS", tc.env)
			flags := map[string]any{}
			if tc.yaml != "" {
				file := tempFile(t, tc.yaml)
				t.Cleanup(func() { _ = os.Remove(file) })
				flags[ConfigFlag] = file
			}
			c := setupWithDefaults(flags, t)
			c.SetArgs(tc.args)
			err := c.Execute()
			if tc.err != "" {
				ErrContains(t, tc.err, err)
				ErrContains(t, "https://www.runatlantis.io/versions/main/docs/experiments.html", err)
				Assert(t, !strings.Contains(err.Error(), "in --experiments"), "error assumes the setting came from a flag")
				return
			}
			Ok(t, err)
			Equals(t, len(tc.want), len(passedConfig.Experiments))
			for i, name := range tc.want {
				Equals(t, name, passedConfig.Experiments[i])
			}
			Assert(t, !passedConfig.EnableDriftDetection, "permission enabled drift detection")
			Assert(t, !passedConfig.EnableDriftRemediation, "permission enabled remediation")
		})
	}
}
