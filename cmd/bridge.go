package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
  Use --include and --exclude flags to control which registers are synced.
  Patterns use standard glob syntax (* matches any sequence, ? matches one char).
  Exclude patterns take precedence over include patterns.
  If no filters specified, all registers are synced.

Examples:
  # Sync all registers
  export MQTT=localhost:1883
  export REGISTRY=http://localhost:8080
  mreg bridge --ttl 30s

  # Sync only temperature sensors
  mreg bridge --include "temp*" --include "*-temperature"

  # Sync all sensors except debug sensors
  mreg bridge --include "sensors/*" --exclude "sensors/debug/*"

  # Multiple patterns with custom batch interval
  mreg bridge --include "sensor-*" --exclude "*-test" --batch-interval 500ms`,
	RunE: runBridge,
}

func init() {
	RootCmd.AddCommand(bridgeCmd)
	bridgeCmd.Flags().Duration("ttl", 10*time.Second, "TTL for registers in REST registry")
	bridgeCmd.Flags().StringSlice("include", []string{}, "Glob patterns for registers to include")
	bridgeCmd.Flags().StringSlice("exclude", []string{}, "Glob patterns for registers to exclude")
	bridgeCmd.Flags().Duration("batch-interval", 200*time.Millisecond, "Interval for batching value updates")
	bridgeCmd.Flags().Duration("poll-interval", 30*time.Second, "Interval for polling REST change requests")
}

// Bridge manages the one-way MQTT to REST synchronization
type Bridge struct {
	mqttRegs        *reg.Registers
	restProvider    *rest.ProviderClient
	ttl             time.Duration
	batchInterval   time.Duration
	pollInterval    time.Duration
	includePatterns []string
	excludePatterns []string

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
	includePatterns, _ := cmd.Flags().GetStringSlice("include")
	excludePatterns, _ := cmd.Flags().GetStringSlice("exclude")

	// Check REGISTRY env var
	registryURL := os.Getenv("REGISTRY")
	if registryURL == "" {
		return fmt.Errorf("REGISTRY environment variable not set")
	}

	log.Printf("Starting MQTT→REST bridge...")
	log.Printf("MQTT broker: %s", os.Getenv("MQTT"))
	log.Printf("REST registry: %s", registryURL)
	log.Printf("TTL: %s, Batch interval: %s, Poll interval: %s", ttl, batchInterval, pollInterval)
	if len(includePatterns) > 0 {
		log.Printf("Include patterns: %v", includePatterns)
	}
	if len(excludePatterns) > 0 {
		log.Printf("Exclude patterns: %v", excludePatterns)
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
		mqttRegs:        mqttRegs,
		restProvider:    restProvider,
		ttl:             ttl,
		batchInterval:   batchInterval,
		pollInterval:    pollInterval,
		includePatterns: includePatterns,
		excludePatterns: excludePatterns,
		registerSyncs:   make(map[string]*registerSync),
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
	if !shouldSyncRegister(md.Name, b.includePatterns, b.excludePatterns) {
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

// shouldSyncRegister checks if a register should be synced based on include/exclude patterns
func shouldSyncRegister(name string, includePatterns, excludePatterns []string) bool {
	// No filters = sync everything
	if len(includePatterns) == 0 && len(excludePatterns) == 0 {
		return true
	}

	// Check excludes first (higher priority)
	for _, pattern := range excludePatterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return false
		}
	}

	// If includes specified, must match at least one
	if len(includePatterns) > 0 {
		for _, pattern := range includePatterns {
			if matched, _ := filepath.Match(pattern, name); matched {
				return true
			}
		}
		return false
	}

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
