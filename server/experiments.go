// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"slices"
	"strings"
)

// ExperimentsDocumentationURL includes experiments not yet in a stable release.
const ExperimentsDocumentationURL = "https://www.runatlantis.io/versions/main/docs/experiments.html"

var knownExperiments = []string{"drift-detection"}

// ValidateExperiments checks server permissions without enabling features.
func (c UserConfig) ValidateExperiments() error {
	for _, name := range c.Experiments {
		if !slices.Contains(knownExperiments, name) {
			return fmt.Errorf("unknown server experiment %q in experiments configuration; supported names are %s; see %s", name, strings.Join(knownExperiments, ", "), ExperimentsDocumentationURL)
		}
	}
	return nil
}
