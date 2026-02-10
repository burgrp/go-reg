package cmd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/burgrp/go-reg/reg"
)

func TestParseFilterRule(t *testing.T) {
	tests := []struct {
		name        string
		spec        string
		expected    filterRule
		expectError bool
	}{
		{
			name:     "include rule",
			spec:     "+temp*",
			expected: filterRule{pattern: "temp*", include: true},
		},
		{
			name:     "exclude rule",
			spec:     "-debug*",
			expected: filterRule{pattern: "debug*", include: false},
		},
		{
			name:        "empty rule",
			spec:        "",
			expectError: true,
		},
		{
			name:        "no prefix",
			spec:        "temp*",
			expectError: true,
		},
		{
			name:     "complex pattern include",
			spec:     "+pv.inverter.*.pvInput",
			expected: filterRule{pattern: "pv.inverter.*.pvInput", include: true},
		},
		{
			name:     "complex pattern exclude",
			spec:     "-pv.inverter.*",
			expected: filterRule{pattern: "pv.inverter.*", include: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseFilterRule(tt.spec)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result.pattern != tt.expected.pattern || result.include != tt.expected.include {
				t.Errorf("parseFilterRule(%q) = %+v, want %+v", tt.spec, result, tt.expected)
			}
		})
	}
}

func TestReadFilterFile(t *testing.T) {
	// Create a temporary filter file
	tmpfile, err := os.CreateTemp("", "filter-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	content := `# This is a comment
+temp*
-debug*

# Another comment
+sensor-*
-*-test
`
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	rules, err := readFilterFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("readFilterFile failed: %v", err)
	}

	expected := []filterRule{
		{pattern: "temp*", include: true},
		{pattern: "debug*", include: false},
		{pattern: "sensor-*", include: true},
		{pattern: "*-test", include: false},
	}

	if len(rules) != len(expected) {
		t.Fatalf("expected %d rules, got %d", len(expected), len(rules))
	}

	for i, rule := range rules {
		if rule.pattern != expected[i].pattern || rule.include != expected[i].include {
			t.Errorf("rule %d: got %+v, want %+v", i, rule, expected[i])
		}
	}
}

func TestParseFilterRules(t *testing.T) {
	// Create a temporary filter file
	tmpfile, err := os.CreateTemp("", "filter-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	content := `+file-rule-1
-file-rule-2
`
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	specs := []string{
		"+inline-rule-1",
		"-inline-rule-2",
		"@" + tmpfile.Name(),
		"+inline-rule-3",
	}

	rules, err := parseFilterRules(specs)
	if err != nil {
		t.Fatalf("parseFilterRules failed: %v", err)
	}

	expected := []filterRule{
		{pattern: "inline-rule-1", include: true},
		{pattern: "inline-rule-2", include: false},
		{pattern: "file-rule-1", include: true},
		{pattern: "file-rule-2", include: false},
		{pattern: "inline-rule-3", include: true},
	}

	if len(rules) != len(expected) {
		t.Fatalf("expected %d rules, got %d", len(expected), len(rules))
	}

	for i, rule := range rules {
		if rule.pattern != expected[i].pattern || rule.include != expected[i].include {
			t.Errorf("rule %d: got %+v, want %+v", i, rule, expected[i])
		}
	}
}

func TestShouldSyncRegister(t *testing.T) {
	tests := []struct {
		name         string
		registerName string
		rules        []filterRule
		expected     bool
	}{
		{
			name:         "no rules - include by default",
			registerName: "temp",
			rules:        []filterRule{},
			expected:     true,
		},
		{
			name:         "include rule matches",
			registerName: "temp1",
			rules: []filterRule{
				{pattern: "temp*", include: true},
			},
			expected: true,
		},
		{
			name:         "include rule does not match",
			registerName: "humidity",
			rules: []filterRule{
				{pattern: "temp*", include: true},
			},
			expected: true, // Default is include when no match
		},
		{
			name:         "exclude rule matches",
			registerName: "temp-debug",
			rules: []filterRule{
				{pattern: "*-debug", include: false},
			},
			expected: false,
		},
		{
			name:         "first rule wins - exclude",
			registerName: "temp-debug",
			rules: []filterRule{
				{pattern: "*-debug", include: false},
				{pattern: "temp*", include: true},
			},
			expected: false,
		},
		{
			name:         "first rule wins - include",
			registerName: "temp-debug",
			rules: []filterRule{
				{pattern: "temp*", include: true},
				{pattern: "*-debug", include: false},
			},
			expected: true,
		},
		{
			name:         "complex use case - exclude inverter but include pvInput",
			registerName: "pv.inverter.1.power",
			rules: []filterRule{
				{pattern: "pv.inverter.*", include: false},
				{pattern: "pv.inverter.*.pvInput", include: true},
			},
			expected: false, // First rule matches and excludes
		},
		{
			name:         "complex use case - include pvInput specifically",
			registerName: "pv.inverter.1.pvInput",
			rules: []filterRule{
				{pattern: "pv.inverter.*", include: false},
				{pattern: "pv.inverter.*.pvInput", include: true},
			},
			expected: false, // First rule matches (pv.inverter.* matches pv.inverter.1.pvInput)
		},
		{
			name:         "correct order - specific before general",
			registerName: "pv.inverter.1.pvInput",
			rules: []filterRule{
				{pattern: "pv.inverter.*.pvInput", include: true},
				{pattern: "pv.inverter.*", include: false},
			},
			expected: true, // First rule matches and includes
		},
		{
			name:         "correct order - general exclude then specific include",
			registerName: "pv.inverter.1.power",
			rules: []filterRule{
				{pattern: "pv.inverter.*.pvInput", include: true},
				{pattern: "pv.inverter.*", include: false},
			},
			expected: false, // Second rule matches and excludes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSyncRegister(tt.registerName, tt.rules)
			if result != tt.expected {
				t.Errorf("shouldSyncRegister(%q, %+v) = %v, want %v",
					tt.registerName, tt.rules, result, tt.expected)
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

func TestFilterRulesExample(t *testing.T) {
	// Test the user's specific use case:
	// - Include all by default
	// - Exclude pv.inverter.*
	// - But include pv.inverter.*.pvInput

	// Correct order: specific patterns before general patterns
	rules := []filterRule{
		{pattern: "pv.inverter.*.pvInput", include: true},  // Specific: include pvInput
		{pattern: "pv.inverter.*", include: false},         // General: exclude all inverter
	}

	tests := []struct {
		name     string
		register string
		expected bool
	}{
		{"random register", "temperature", true},           // No match, default include
		{"inverter power", "pv.inverter.1.power", false},   // Matches general exclude
		{"inverter pvInput", "pv.inverter.1.pvInput", true}, // Matches specific include
		{"inverter voltage", "pv.inverter.2.voltage", false}, // Matches general exclude
		{"other pv register", "pv.battery.charge", true},   // No match, default include
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSyncRegister(tt.register, rules)
			if result != tt.expected {
				t.Errorf("shouldSyncRegister(%q) = %v, want %v (with rules %+v)",
					tt.register, result, tt.expected, rules)
			}
		})
	}
}

func TestFilterFileExample(t *testing.T) {
	// Create example filter file
	tmpfile, err := os.CreateTemp("", "filter-example-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	content := `# Exclude all pv.inverter.* registers
# But include the specific pvInput registers
+pv.inverter.*.pvInput
-pv.inverter.*
`
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// Read and parse
	rules, err := readFilterFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("readFilterFile failed: %v", err)
	}

	// Test the rules
	tests := []struct {
		register string
		expected bool
	}{
		{"pv.inverter.1.power", false},
		{"pv.inverter.1.pvInput", true},
		{"pv.inverter.2.voltage", false},
		{"pv.inverter.2.pvInput", true},
		{"other.register", true},
	}

	for _, tt := range tests {
		result := shouldSyncRegister(tt.register, rules)
		if result != tt.expected {
			t.Errorf("shouldSyncRegister(%q) = %v, want %v",
				tt.register, result, tt.expected)
		}
	}
}
