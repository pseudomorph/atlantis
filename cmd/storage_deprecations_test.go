// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	. "github.com/runatlantis/atlantis/testing"
)

func TestStorageDeprecationWarningsAcrossConfigurationSources(t *testing.T) {
	for _, source := range []string{"flag", "environment", "yaml"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("ATLANTIS_REDIS_PASSWORD", "")
			flags := map[string]any{}
			arguments := []string{}
			switch source {
			case "flag":
				arguments = []string{"--redis-password=never-print-this"}
			case "environment":
				t.Setenv("ATLANTIS_REDIS_PASSWORD", "never-print-this")
			case "yaml":
				filename := tempFile(t, "redis-password: never-print-this\n")
				t.Cleanup(func() { _ = os.Remove(filename) })
				flags[ConfigFlag] = filename
			}
			command := setupWithDefaults(flags, t)
			command.SetArgs(arguments)
			reader, writer, err := os.Pipe()
			Ok(t, err)
			original := os.Stdout
			os.Stdout = writer
			defer func() { os.Stdout = original }()
			err = command.Execute()
			os.Stdout = original
			Ok(t, writer.Close())
			output, readErr := io.ReadAll(reader)
			Ok(t, readErr)
			Ok(t, reader.Close())
			Ok(t, err)
			Assert(t, strings.Contains(string(output), "--redis-password has been deprecated"), "missing deprecation warning")
			Assert(t, !strings.Contains(string(output), "never-print-this"), "warning exposed a credential")
		})
	}
}

func TestLegacyStorageFlagsRemainDiscoverableAsDeprecated(t *testing.T) {
	command := setupWithDefaults(map[string]any{}, t)
	for _, name := range legacyStorageFlags {
		flag := command.Flags().Lookup(name)
		Assert(t, flag != nil, "legacy flag removed before graduation")
		Assert(t, strings.Contains(flag.Usage, "Deprecated"), "flag lacks deprecation guidance: %s", name)
	}
}
