package cmd

import (
	"encoding/json"
	"testing"

	"github.com/burgrp/go-reg/reg"
)

func TestShouldSyncRegister(t *testing.T) {
	tests := []struct {
		name            string
		registerName    string
		includePatterns []string
		excludePatterns []string
		expected        bool
	}{
		{
			name:            "no filters - sync everything",
			registerName:    "temp",
			includePatterns: []string{},
			excludePatterns: []string{},
			expected:        true,
		},
		{
			name:            "include pattern matches",
			registerName:    "temp1",
			includePatterns: []string{"temp*"},
			excludePatterns: []string{},
			expected:        true,
		},
		{
			name:            "include pattern does not match",
			registerName:    "humidity",
			includePatterns: []string{"temp*"},
			excludePatterns: []string{},
			expected:        false,
		},
		{
			name:            "exclude pattern matches",
			registerName:    "temp-debug",
			includePatterns: []string{},
			excludePatterns: []string{"*-debug"},
			expected:        false,
		},
		{
			name:            "exclude takes precedence over include",
			registerName:    "temp-debug",
			includePatterns: []string{"temp*"},
			excludePatterns: []string{"*-debug"},
			expected:        false,
		},
		{
			name:            "multiple includes, one matches",
			registerName:    "humidity",
			includePatterns: []string{"temp*", "hum*"},
			excludePatterns: []string{},
			expected:        true,
		},
		{
			name:            "multiple includes, none match",
			registerName:    "pressure",
			includePatterns: []string{"temp*", "hum*"},
			excludePatterns: []string{},
			expected:        false,
		},
		{
			name:            "complex pattern - sensors path",
			registerName:    "sensors/temp",
			includePatterns: []string{"sensors/*"},
			excludePatterns: []string{},
			expected:        true,
		},
		{
			name:            "complex pattern - exclude debug path",
			registerName:    "sensors/debug/test",
			includePatterns: []string{"sensors/*"},
			excludePatterns: []string{"sensors/debug/*"},
			expected:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSyncRegister(tt.registerName, tt.includePatterns, tt.excludePatterns)
			if result != tt.expected {
				t.Errorf("shouldSyncRegister(%q, %v, %v) = %v, want %v",
					tt.registerName, tt.includePatterns, tt.excludePatterns, result, tt.expected)
			}
		})
	}
}

func TestMetadataEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        map[string]any
		b        map[string]any
		expected bool
	}{
		{
			name:     "both empty",
			a:        map[string]any{},
			b:        map[string]any{},
			expected: true,
		},
		{
			name:     "both nil treated as empty",
			a:        nil,
			b:        nil,
			expected: true,
		},
		{
			name:     "simple string metadata equal",
			a:        map[string]any{"device": "sensor1"},
			b:        map[string]any{"device": "sensor1"},
			expected: true,
		},
		{
			name:     "simple string metadata not equal",
			a:        map[string]any{"device": "sensor1"},
			b:        map[string]any{"device": "sensor2"},
			expected: false,
		},
		{
			name:     "different keys",
			a:        map[string]any{"device": "sensor1"},
			b:        map[string]any{"name": "sensor1"},
			expected: false,
		},
		{
			name:     "different lengths",
			a:        map[string]any{"device": "sensor1", "location": "room1"},
			b:        map[string]any{"device": "sensor1"},
			expected: false,
		},
		{
			name: "complex metadata equal",
			a: map[string]any{
				"device":   "sensor1",
				"location": "room1",
				"port":     float64(8080),
			},
			b: map[string]any{
				"device":   "sensor1",
				"location": "room1",
				"port":     float64(8080),
			},
			expected: true,
		},
		{
			name: "complex metadata not equal",
			a: map[string]any{
				"device":   "sensor1",
				"location": "room1",
				"port":     float64(8080),
			},
			b: map[string]any{
				"device":   "sensor1",
				"location": "room2",
				"port":     float64(8080),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := metadataEqual(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("metadataEqual(%v, %v) = %v, want %v",
					tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestMQTTMetadataToREST(t *testing.T) {
	tests := []struct {
		name     string
		mqtt     reg.Metadata
		expected map[string]any
	}{
		{
			name:     "empty metadata",
			mqtt:     reg.Metadata{},
			expected: map[string]any{},
		},
		{
			name: "simple metadata",
			mqtt: reg.Metadata{
				"device":   "sensor1",
				"location": "room1",
			},
			expected: map[string]any{
				"device":   "sensor1",
				"location": "room1",
			},
		},
		{
			name: "metadata with numbers",
			mqtt: reg.Metadata{
				"device": "sensor1",
				"port":   "8080",
			},
			expected: map[string]any{
				"device": "sensor1",
				"port":   "8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mqttMetadataToREST(tt.mqtt)
			if !metadataEqual(result, tt.expected) {
				t.Errorf("mqttMetadataToREST(%v) = %v, want %v",
					tt.mqtt, result, tt.expected)
			}
		})
	}
}

func TestMQTTValueToREST(t *testing.T) {
	tests := []struct {
		name     string
		mqtt     []byte
		expected any
	}{
		{
			name:     "empty bytes",
			mqtt:     []byte{},
			expected: nil,
		},
		{
			name:     "nil bytes",
			mqtt:     nil,
			expected: nil,
		},
		{
			name:     "number value",
			mqtt:     []byte("22.5"),
			expected: float64(22.5),
		},
		{
			name:     "string value",
			mqtt:     []byte(`"hello"`),
			expected: "hello",
		},
		{
			name:     "boolean value",
			mqtt:     []byte("true"),
			expected: true,
		},
		{
			name:     "object value",
			mqtt:     []byte(`{"temp":22.5,"unit":"C"}`),
			expected: map[string]any{"temp": float64(22.5), "unit": "C"},
		},
		{
			name:     "array value",
			mqtt:     []byte(`[1,2,3]`),
			expected: []any{float64(1), float64(2), float64(3)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mqttValueToREST(tt.mqtt)
			resultJSON, _ := json.Marshal(result)
			expectedJSON, _ := json.Marshal(tt.expected)
			if string(resultJSON) != string(expectedJSON) {
				t.Errorf("mqttValueToREST(%s) = %v, want %v",
					tt.mqtt, result, tt.expected)
			}
		})
	}
}

func TestRESTValueToMQTT(t *testing.T) {
	tests := []struct {
		name     string
		rest     any
		expected string
	}{
		{
			name:     "nil value",
			rest:     nil,
			expected: "",
		},
		{
			name:     "number value",
			rest:     float64(22.5),
			expected: "22.5",
		},
		{
			name:     "string value",
			rest:     "hello",
			expected: `"hello"`,
		},
		{
			name:     "boolean value",
			rest:     true,
			expected: "true",
		},
		{
			name:     "object value",
			rest:     map[string]any{"temp": float64(22.5), "unit": "C"},
			expected: `{"temp":22.5,"unit":"C"}`,
		},
		{
			name:     "array value",
			rest:     []any{float64(1), float64(2), float64(3)},
			expected: `[1,2,3]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := restValueToMQTT(tt.rest)
			if string(result) != tt.expected {
				t.Errorf("restValueToMQTT(%v) = %s, want %s",
					tt.rest, string(result), tt.expected)
			}
		})
	}
}

func TestMQTTValueToRESTAndBack(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"number", float64(42)},
		{"string", "hello world"},
		{"boolean", true},
		{"object", map[string]any{"key": "value", "num": float64(123)}},
		{"array", []any{float64(1), "two", true}},
		{"nested", map[string]any{"outer": map[string]any{"inner": float64(42)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert to MQTT (bytes)
			mqttBytes := restValueToMQTT(tt.value)

			// Convert back to REST
			result := mqttValueToREST(mqttBytes)

			// Compare JSON representations
			originalJSON, _ := json.Marshal(tt.value)
			resultJSON, _ := json.Marshal(result)

			if string(originalJSON) != string(resultJSON) {
				t.Errorf("Round-trip failed: %v -> %s -> %v",
					tt.value, mqttBytes, result)
			}
		})
	}
}
