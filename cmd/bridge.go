package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/burgrp/go-reg/reg"
	"github.com/burgrp/reg/pkg/wire/rest"
	"github.com/spf13/cobra"
)

var bridgeCmd = &cobra.Command{
	Use:   "bridge",
	Short: "Bridge MQTT registers to REST registry",
	Long: `Runs a one-way bridge that synchronizes MQTT registers to REST registry.

MQTT registers (metadata and values) are automatically provided to REST.
Change requests from REST consumers are forwarded back to MQTT providers.

The bridge watches MQTT for register advertisements and creates corresponding
REST providers. Values are batched for optimal performance.

The REGISTRY environment variable must be set to the REST registry URL.

Filtering:
  Use --filter flag to control which registers are synced with ordered rules.
  Rules are evaluated in order, first match wins. Default is include if no match.

  Rule format:
    +pattern  - Include registers matching pattern
    -pattern  - Exclude registers matching pattern
    @file     - Read rules from file (one per line, # for comments)

  Patterns use standard glob syntax (* matches any sequence, ? matches one char).

Examples:
  # Sync all registers (no filters)
  export MQTT=localhost:1883
  export REGISTRY=http://localhost:8080
  mreg bridge --ttl 30s

  # Exclude pv.inverter.* but include pv.inverter.*.pvInput
  # IMPORTANT: Specific patterns BEFORE general patterns!
  mreg bridge \
    --filter "+pv.inverter.*.pvInput" \
    --filter "-pv.inverter.*"

  # Include only sensors, exclude debug
  mreg bridge \
    --filter "+sensor-*" \
    --filter "-*-debug"

  # Read filter rules from file
  mreg bridge --filter "@filter-rules.txt"

  # filter-rules.txt example:
  # # Specific patterns first!
  # +pv.inverter.*.pvInput
  # -pv.inverter.*
  # -*.debug`,
	RunE: runBridge,
}

func init() {
	RootCmd.AddCommand(bridgeCmd)
	bridgeCmd.Flags().Duration("ttl", 10*time.Second, "TTL for registers in REST registry")
	bridgeCmd.Flags().StringSlice("filter", []string{}, "Filter rules (+pattern to include, -pattern to exclude, @file to read from file)")
	bridgeCmd.Flags().Duration("batch-interval", 200*time.Millisecond, "Interval for batching value updates")
	bridgeCmd.Flags().Duration("poll-interval", 30*time.Second, "Interval for polling REST change requests")
}

// filterRule represents a single include/exclude rule
type filterRule struct {
	pattern string
	include bool // true = include, false = exclude
}

// Bridge manages the one-way MQTT to REST synchronization
type Bridge struct {
	mqttRegs      *reg.Registers
	restProvider  *rest.ProviderClient
	ttl           time.Duration
	batchInterval time.Duration
	pollInterval  time.Duration
	filterRules   []filterRule

	// State
	registerSyncs map[string]*registerSync
	syncMu        sync.RWMutex
	batchMgr      *batchManager
}

// registerSync tracks the state of a single synced register
type registerSync struct {
	name         string
	mqttMetadata reg.Metadata
	restMetadata map[string]any
	mqttConsumer <-chan []byte // For receiving MQTT set requests
	mqttProvider chan<- []byte // For forwarding REST change requests to MQTT
	cancel       context.CancelFunc
}

// batchManager handles batching of value updates for performance
type batchManager struct {
	pending   map[string]*batchEntry
	mu        sync.Mutex
	provider  *rest.ProviderClient
	ttl       time.Duration
	registerSyncs map[string]*registerSync
	syncMu    *sync.RWMutex
}

type batchEntry struct {
	value    any
	metadata map[string]any
}

func runBridge(cmd *cobra.Command, args []string) error {
	// Parse flags
	ttl, _ := cmd.Flags().GetDuration("ttl")
	batchInterval, _ := cmd.Flags().GetDuration("batch-interval")
	pollInterval, _ := cmd.Flags().GetDuration("poll-interval")
	filterSpecs, _ := cmd.Flags().GetStringSlice("filter")

	// Parse filter rules
	filterRules, err := parseFilterRules(filterSpecs)
	if err != nil {
		return fmt.Errorf("invalid filter rules: %w", err)
	}

	// Check REGISTRY env var
	registryURL := os.Getenv("REGISTRY")
	if registryURL == "" {
		return fmt.Errorf("REGISTRY environment variable not set")
	}

	log.Printf("Starting MQTT→REST bridge...")
	log.Printf("MQTT broker: %s", os.Getenv("MQTT"))
	log.Printf("REST registry: %s", registryURL)
	log.Printf("TTL: %s, Batch interval: %s, Poll interval: %s", ttl, batchInterval, pollInterval)
	if len(filterRules) > 0 {
		log.Printf("Filter rules: %d rules loaded", len(filterRules))
		for i, rule := range filterRules {
			action := "include"
			if !rule.include {
				action = "exclude"
			}
			log.Printf("  %d. %s: %s", i+1, action, rule.pattern)
		}
	}

	// Connect to MQTT
	mqttRegs, err := reg.NewRegisters()
	if err != nil {
		return fmt.Errorf("failed to connect to MQTT: %w", err)
	}
	log.Println("Connected to MQTT broker")

	// Create REST provider client
	restProvider := rest.NewProviderClient(registryURL)
	log.Println("Connected to REST registry")

	// Create bridge
	bridge := &Bridge{
		mqttRegs:      mqttRegs,
		restProvider:  restProvider,
		ttl:           ttl,
		batchInterval: batchInterval,
		pollInterval:  pollInterval,
		filterRules:   filterRules,
		registerSyncs: make(map[string]*registerSync),
	}

	// Initialize batch manager
	bridge.batchMgr = &batchManager{
		pending:       make(map[string]*batchEntry),
		provider:      restProvider,
		ttl:           ttl,
		registerSyncs: bridge.registerSyncs,
		syncMu:        &bridge.syncMu,
	}

	// Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down bridge...")
		cancel()
	}()

	// Run the bridge
	return bridge.Run(ctx)
}

// Run starts the bridge main loop
func (b *Bridge) Run(ctx context.Context) error {
	// Watch MQTT for metadata and values
	mqttMetadata, mqttValues := reg.Watch(b.mqttRegs)
	log.Println("Watching MQTT registers...")

	// Start batch flusher
	go b.flushBatchPeriodically(ctx)

	// Main event loop
	for {
		select {
		case <-ctx.Done():
			log.Println("Bridge stopped")
			return nil

		case md, ok := <-mqttMetadata:
			if !ok {
				log.Println("MQTT metadata channel closed")
				return nil
			}
			b.handleMetadata(ctx, md)

		case val, ok := <-mqttValues:
			if !ok {
				log.Println("MQTT values channel closed")
				return nil
			}
			b.handleValue(val)
		}
	}
}

// handleMetadata processes MQTT register metadata (discovery)
func (b *Bridge) handleMetadata(ctx context.Context, md reg.NameAndMetadata) {
	// Apply filters
	if !shouldSyncRegister(md.Name, b.filterRules) {
		return
	}

	b.syncMu.Lock()
	existingSync, exists := b.registerSyncs[md.Name]
	b.syncMu.Unlock()

	if exists {
		// Check for metadata changes
		restMetadata := mqttMetadataToREST(md.Metadata)
		if metadataEqual(existingSync.restMetadata, restMetadata) {
			return // No change
		}
		log.Printf("Metadata changed for %s, recreating sync", md.Name)
		existingSync.cancel()
		b.syncMu.Lock()
		delete(b.registerSyncs, md.Name)
		b.syncMu.Unlock()
	}

	log.Printf("Syncing MQTT register: %s", md.Name)

	// Create context for this register
	regCtx, regCancel := context.WithCancel(ctx)

	// Convert metadata
	restMetadata := mqttMetadataToREST(md.Metadata)

	// Create MQTT consumer to receive set requests from MQTT providers
	mqttReaderStr, mqttWriterStr := reg.ConsumeString(b.mqttRegs, md.Name)

	// Convert string channels to byte channels
	mqttReader := make(chan []byte, 1)
	mqttWriter := make(chan []byte, 1)

	go func() {
		for str := range mqttReaderStr {
			mqttReader <- []byte(str)
		}
		close(mqttReader)
	}()

	go func() {
		for b := range mqttWriter {
			mqttWriterStr <- string(b)
		}
		close(mqttWriterStr)
	}()

	// Create sync entry
	sync := &registerSync{
		name:         md.Name,
		mqttMetadata: md.Metadata,
		restMetadata: restMetadata,
		mqttConsumer: mqttReader,
		mqttProvider: mqttWriter,
		cancel:       regCancel,
	}

	b.syncMu.Lock()
	b.registerSyncs[md.Name] = sync
	b.syncMu.Unlock()

	// Start polling for REST change requests and forward to MQTT
	go b.pollChangeRequests(regCtx, md.Name, mqttWriter)

	log.Printf("Started sync for %s", md.Name)
}

// handleValue processes MQTT register value updates
func (b *Bridge) handleValue(val reg.NameAndValue) {
	b.syncMu.RLock()
	sync, exists := b.registerSyncs[val.Name]
	b.syncMu.RUnlock()

	if !exists {
		return // Register not synced
	}

	// Convert value
	restValue := mqttValueToREST(val.Value)

	// Add to batch
	b.batchMgr.mu.Lock()
	b.batchMgr.pending[val.Name] = &batchEntry{
		value:    restValue,
		metadata: sync.restMetadata,
	}
	b.batchMgr.mu.Unlock()
}

// flushBatchPeriodically flushes batched updates at regular intervals
func (b *Bridge) flushBatchPeriodically(ctx context.Context) {
	ticker := time.NewTicker(b.batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flushBatch(ctx)
		}
	}
}

// flushBatch sends all pending updates to REST using the batch API
func (b *Bridge) flushBatch(ctx context.Context) {
	b.batchMgr.mu.Lock()
	pending := b.batchMgr.pending
	b.batchMgr.pending = make(map[string]*batchEntry)
	b.batchMgr.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	// Build batch request
	updates := make(map[string]rest.RegisterUpdate)
	for name, entry := range pending {
		updates[name] = rest.RegisterUpdate{
			Value:    entry.value,
			Metadata: entry.metadata,
			TTL:      b.ttl,
		}
	}

	// Send to REST
	if err := b.restProvider.SetRegisters(ctx, updates); err != nil {
		log.Printf("Warning: batch update failed: %v", err)
		return
	}

	log.Printf("Flushed batch: %d registers", len(pending))
}

// pollChangeRequests polls REST for consumer change requests and forwards to MQTT
func (b *Bridge) pollChangeRequests(ctx context.Context, name string, mqttWriter chan<- []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Poll for change requests
		requests, err := b.restProvider.GetChangeRequests(ctx, []string{name}, b.pollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Error polling change requests for %s: %v", name, err)
			time.Sleep(1 * time.Second)
			continue
		}

		// Forward to MQTT if present
		if value, exists := requests[name]; exists {
			reqBytes := restValueToMQTT(value)
			select {
			case mqttWriter <- reqBytes:
				log.Printf("Forwarded change request to MQTT: %s = %v", name, value)
			case <-ctx.Done():
				return
			default:
				log.Printf("Warning: failed to forward change request for %s", name)
			}
		}
	}
}

// parseFilterRules parses filter specifications into rules
func parseFilterRules(specs []string) ([]filterRule, error) {
	var rules []filterRule

	for _, spec := range specs {
		if strings.HasPrefix(spec, "@") {
			// Read from file
			fileRules, err := readFilterFile(spec[1:])
			if err != nil {
				return nil, fmt.Errorf("reading filter file %s: %w", spec[1:], err)
			}
			rules = append(rules, fileRules...)
		} else {
			// Parse single rule
			rule, err := parseFilterRule(spec)
			if err != nil {
				return nil, err
			}
			rules = append(rules, rule)
		}
	}

	return rules, nil
}

// parseFilterRule parses a single filter rule
func parseFilterRule(spec string) (filterRule, error) {
	if len(spec) == 0 {
		return filterRule{}, fmt.Errorf("empty filter rule")
	}

	switch spec[0] {
	case '+':
		return filterRule{pattern: spec[1:], include: true}, nil
	case '-':
		return filterRule{pattern: spec[1:], include: false}, nil
	default:
		return filterRule{}, fmt.Errorf("filter rule must start with + or -: %s", spec)
	}
}

// readFilterFile reads filter rules from a file
func readFilterFile(path string) ([]filterRule, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rules []filterRule
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule, err := parseFilterRule(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		rules = append(rules, rule)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return rules, nil
}

// shouldSyncRegister checks if a register should be synced based on filter rules
func shouldSyncRegister(name string, rules []filterRule) bool {
	// If no rules, include by default
	if len(rules) == 0 {
		return true
	}

	// Evaluate rules in order, first match wins
	for _, rule := range rules {
		matched, _ := filepath.Match(rule.pattern, name)
		if matched {
			return rule.include
		}
	}

	// No match, include by default
	return true
}

// metadataEqual compares two metadata maps for equality
func metadataEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

// mqttMetadataToREST converts MQTT metadata to REST format
func mqttMetadataToREST(mqtt reg.Metadata) map[string]any {
	rest := make(map[string]any)
	for k, v := range mqtt {
		rest[k] = v
	}
	return rest
}

// mqttValueToREST converts MQTT byte value to REST any value
func mqttValueToREST(mqttBytes []byte) any {
	if len(mqttBytes) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(mqttBytes, &value); err != nil {
		log.Printf("Error parsing value: %v", err)
		return nil
	}
	return value
}

// restValueToMQTT converts REST any value to MQTT byte value
func restValueToMQTT(restValue any) []byte {
	if restValue == nil {
		return []byte{}
	}
	bytes, err := json.Marshal(restValue)
	if err != nil {
		log.Printf("Error marshaling value: %v", err)
		return []byte{}
	}
	return bytes
}
