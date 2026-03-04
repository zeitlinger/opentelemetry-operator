// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeclarativeConfig_DeepCopy_NestedMaps(t *testing.T) {
	original := &DeclarativeConfig{
		Object: map[string]any{
			"file_format": "1.0",
			"exporters": map[string]any{
				"otlp": map[string]any{
					"endpoint": "http://collector:4318",
					"headers": map[string]any{
						"x-token": "${API_KEY}",
					},
				},
			},
			"tags": []any{"prod", "us-east"},
		},
	}

	copied := original.DeepCopy()

	// Mutate the copy's nested map.
	exporters := copied.Object["exporters"].(map[string]any)
	otlp := exporters["otlp"].(map[string]any)
	otlp["endpoint"] = "http://changed:4318"
	headers := otlp["headers"].(map[string]any)
	headers["x-new"] = "added"

	// Mutate the copy's slice.
	tags := copied.Object["tags"].([]any)
	tags[0] = "staging"

	// Original must be unaffected.
	origExporters := original.Object["exporters"].(map[string]any)
	origOtlp := origExporters["otlp"].(map[string]any)
	assert.Equal(t, "http://collector:4318", origOtlp["endpoint"])
	origHeaders := origOtlp["headers"].(map[string]any)
	assert.Equal(t, "${API_KEY}", origHeaders["x-token"])
	_, hasNew := origHeaders["x-new"]
	assert.False(t, hasNew, "mutation of copy should not affect original")

	origTags := original.Object["tags"].([]any)
	assert.Equal(t, "prod", origTags[0])
}

func TestDeclarativeConfig_DeepCopy_Nil(t *testing.T) {
	var d *DeclarativeConfig
	assert.Nil(t, d.DeepCopy())
}
