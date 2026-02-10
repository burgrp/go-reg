package cmd

import (
	"bytes"
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
	Long: `Runs a bidirectional bridge that synchronizes registers between MQTT and REST registry.

Registers on MQTT will appear in REST registry and vice versa.
Value changes on either side propagate to the other in real-time.
Change requests work bidirectionally with loop prevention.
Metadata changes are synchronized automatically.

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

  # Multiple patterns
  mreg bridge --include "sensor-*" --include "actuator-*" --exclude "*-test"`,
	RunE: runBridge,
}

var verbose bool

func init() {
	RootCmd.AddCommand(bridgeCmd)
	bridgeCmd.Flags().Duration("ttl", 10*time.Second, "Default TTL for registers in both registries")
	bridgeCmd.Flags().StringSlice("include", []string{}, "Glob patterns for registers to include (can be specified multiple times)")
	bridgeCmd.Flags().StringSlice("exclude", []string{}, "Glob patterns for registers to exclude (can be specified multiple times)")
	bridgeCmd.Flags().Duration("batch-interval", 200*time.Millisecond, "Batch interval for MQTT to REST updates")
}

func logVerbose(format string, args ...any) {
	if verbose {
		log.Printf(format, args...)
	}
}

func logInfo(format string, args ...any) {
	log.Printf(format, args...)
}

type registerSync struct {
	name string

	// MQTT side
	mqttMetadata reg.Metadata
	mqttProvider chan<- []byte // Write to MQTT
	mqttConsumer <-chan []byte // Read from MQTT

	// REST side
	restMetadata map[string]any // Track REST metadata for changes

	// Loop prevention
	lastMQTTValue  []byte    // Track last value from MQTT
	lastRESTValue  []byte    // Track last value from REST
	lastPropagated time.Time // Debouncing timestamp
	mu             sync.Mutex

	cancel context.CancelFunc
}

// batchManager holds pending MQTT to REST updates
type batchManager struct {
	mu              sync.Mutex
	pending         map[string]*batchEntry
	providerClient  *rest.ProviderClient
	registerSyncs   map[string]*registerSync
	defaultTTL      time.Duration
}

type batchEntry struct {
	value    any
	metadata map[string]any
	ttl      time.Duration
}

// restRegisterUpdate represents a REST register update
type restRegisterUpdate struct {
	Name     string
	Value    any
	Metadata map[string]any
}

func runBridge(cmd *cobra.Command, args []string) error {
	// Check VERBOSE environment variable
	verbose = os.Getenv("VERBOSE") != ""

	// 1. Get flags
	ttl, err := cmd.Flags().GetDuration("ttl")
	if err != nil {
		return err
	}

	batchInterval, err := cmd.Flags().GetDuration("batch-interval")
	if err != nil {
		return err
	}

	includePatterns, err := cmd.Flags().GetStringSlice("include")
	if err != nil {
		return err
	}

	excludePatterns, err := cmd.Flags().GetStringSlice("exclude")
	if err != nil {
		return err
	}

	// 2. Check REGISTRY env var
	if os.Getenv("REGISTRY") == "" {
		return fmt.Errorf("REGISTRY environment variable not set")
	}

	logInfo("Starting bidirectional MQTT↔REST bridge...")
	logInfo("MQTT broker: %s", os.Getenv("MQTT"))
	logInfo("REST registry: %s", os.Getenv("REGISTRY"))
	logInfo("Default TTL: %s", ttl)
	logInfo("Batch interval: %s", batchInterval)
	if len(includePatterns) > 0 {
		logInfo("Include patterns: %v", includePatterns)
	}
	if len(excludePatterns) > 0 {
		logInfo("Exclude patterns: %v", excludePatterns)
	}

	// 3. Connect to MQTT
	mqttRegs, err := reg.NewRegisters()
	if err != nil {
		return fmt.Errorf("failed to connect to MQTT: %w", err)
	}
	logInfo("Connected to MQTT broker")

	// 4. Connect to REST using low-level clients
	registryURL := os.Getenv("REGISTRY")
	consumerClient := rest.NewConsumerClient(registryURL)
	providerClient := rest.NewProviderClient(registryURL)
	logInfo("Connected to REST registry")

	// 5. Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		logInfo("Shutting down bridge...")
		cancel()
	}()

	// 6. Watch MQTT registers
	mqttMetadata, mqttValues := reg.Watch(mqttRegs)
	logInfo("Watching MQTT registers...")

	// 7. Start REST consumer poller
	restUpdates := make(chan restRegisterUpdate, 100)
	go pollRESTRegisters(ctx, consumerClient, restUpdates)
	logInfo("Watching REST registers...")

	// 8. Track active syncs
	registerSyncs := make(map[string]*registerSync)
	var syncMu sync.Mutex

	// 9. Setup batch manager for MQTT to REST updates
	batchMgr := &batchManager{
		pending:        make(map[string]*batchEntry),
		providerClient: providerClient,
		registerSyncs:  registerSyncs,
		defaultTTL:     ttl,
	}

	// Start batch flusher
	go func() {
		ticker := time.NewTicker(batchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flushBatch(ctx, batchMgr, &syncMu)
			}
		}
	}()

	// 10. Main loop
	for {
		select {
		case <-ctx.Done():
			logInfo("Bridge stopped")
			return nil

		case md, ok := <-mqttMetadata:
			if !ok {
				logInfo("MQTT metadata channel closed")
				return nil
			}
			handleMQTTMetadata(ctx, md, providerClient, consumerClient, mqttRegs, ttl, includePatterns, excludePatterns, registerSyncs, &syncMu)

		case val, ok := <-mqttValues:
			if !ok {
				logInfo("MQTT values channel closed")
				return nil
			}
			handleMQTTValueBatched(val, registerSyncs, &syncMu, batchMgr)

		case restUpdate, ok := <-restUpdates:
			if !ok {
				logInfo("REST updates channel closed")
				return nil
			}
			handleRESTUpdate(ctx, restUpdate, mqttRegs, providerClient, consumerClient, ttl, includePatterns, excludePatterns, registerSyncs, &syncMu)
		}
	}
}

// pollRESTRegisters continuously polls the REST registry for all register updates
func pollRESTRegisters(ctx context.Context, consumerClient *rest.ConsumerClient, updates chan<- restRegisterUpdate) {
	defer close(updates)

	pollInterval := 5 * time.Second
	var lastRegisters map[string]rest.ConsumerGetRegister

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Long poll for register updates (nil names = all registers)
		registers, err := consumerClient.GetRegisters(ctx, nil, pollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logVerbose("REST poll error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		// Detect changes and new registers
		for name, reg := range registers {
			lastReg, existed := lastRegisters[name]

			// Send update if new register or value/metadata changed
			if !existed || !registersEqual(lastReg, reg) {
				select {
				case updates <- restRegisterUpdate{
					Name:     name,
					Value:    reg.Value,
					Metadata: reg.Metadata,
				}:
					logVerbose("REST register update: %s", name)
				case <-ctx.Done():
					return
				}
			}
		}

		lastRegisters = registers
	}
}

// registersEqual checks if two REST registers are equal
func registersEqual(a, b rest.ConsumerGetRegister) bool {
	// Compare values
	aJSON, _ := json.Marshal(a.Value)
	bJSON, _ := json.Marshal(b.Value)
	if !bytes.Equal(aJSON, bJSON) {
		return false
	}

	// Compare metadata
	return metadataEqual(a.Metadata, b.Metadata)
}

// flushBatch sends all pending MQTT to REST updates using the batch API
func flushBatch(ctx context.Context, batchMgr *batchManager, syncMu *sync.Mutex) {
	batchMgr.mu.Lock()
	pending := batchMgr.pending
	batchMgr.pending = make(map[string]*batchEntry)
	batchMgr.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	logVerbose("Flushing batch with %d updates", len(pending))

	// Build batch request
	updates := make(map[string]rest.RegisterUpdate)
	for name, entry := range pending {
		updates[name] = rest.RegisterUpdate{
			Value:    entry.value,
			Metadata: entry.metadata,
			TTL:      entry.ttl,
		}
	}

	// Send batch update to REST
	err := batchMgr.providerClient.SetRegisters(ctx, updates)
	if err != nil {
		log.Printf("Warning: batch update failed: %v", err)
		return
	}

	// Record propagation for all successfully updated registers
	syncMu.Lock()
	for name, entry := range pending {
		if sync, exists := batchMgr.registerSyncs[name]; exists {
			valueBytes, _ := json.Marshal(entry.value)
			recordPropagation(sync, valueBytes, true)
			logVerbose("Batched sync %s to REST: %v", name, entry.value)
		}
	}
	syncMu.Unlock()
}

// shouldSyncRegister checks if a register should be synced based on include/exclude patterns
func shouldSyncRegister(name string, includePatterns, excludePatterns []string) bool {
	// No filters = sync everything
	if len(includePatterns) == 0 && len(excludePatterns) == 0 {
		return true
	}

	// Check excludes first (higher priority)
	for _, pattern := range excludePatterns {
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			log.Printf("Invalid exclude pattern %q: %v", pattern, err)
			continue
		}
		if matched {
			return false
		}
	}

	// If includes specified, must match at least one
	if len(includePatterns) > 0 {
		for _, pattern := range includePatterns {
			matched, err := filepath.Match(pattern, name)
			if err != nil {
				log.Printf("Invalid include pattern %q: %v", pattern, err)
				continue
			}
			if matched {
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
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// shouldPropagate checks if a value should be propagated to avoid loops
func shouldPropagate(sync *registerSync, newValue []byte, fromMQTT bool) bool {
	sync.mu.Lock()
	defer sync.mu.Unlock()

	// Check debounce window (100ms)
	if time.Since(sync.lastPropagated) < 100*time.Millisecond {
		return false
	}

	// Compare with last value from this direction
	if fromMQTT {
		if bytes.Equal(newValue, sync.lastMQTTValue) {
			return false // Same value, don't propagate
		}
	} else {
		if bytes.Equal(newValue, sync.lastRESTValue) {
			return false // Same value, don't propagate
		}
	}

	return true
}

// recordPropagation records that a value was propagated
func recordPropagation(sync *registerSync, value []byte, fromMQTT bool) {
	sync.mu.Lock()
	defer sync.mu.Unlock()

	if fromMQTT {
		sync.lastMQTTValue = value
	} else {
		sync.lastRESTValue = value
	}
	sync.lastPropagated = time.Now()
}

func handleMQTTMetadata(
	ctx context.Context,
	md reg.NameAndMetadata,
	providerClient *rest.ProviderClient,
	consumerClient *rest.ConsumerClient,
	mqttRegs *reg.Registers,
	ttl time.Duration,
	includePatterns, excludePatterns []string,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	// Check if register should be synced
	if !shouldSyncRegister(md.Name, includePatterns, excludePatterns) {
		return
	}

	syncMu.Lock()
	existingSync, exists := registerSyncs[md.Name]
	syncMu.Unlock()

	if exists {
		// Phase 4: Handle metadata update
		restMetadata := mqttMetadataToREST(md.Metadata)
		if !metadataEqual(existingSync.restMetadata, restMetadata) {
			logVerbose("Metadata changed for MQTT register: %s", md.Name)
			// Cancel existing sync and recreate
			existingSync.cancel()
			syncMu.Lock()
			delete(registerSyncs, md.Name)
			syncMu.Unlock()
			// Fall through to recreate
		} else {
			return
		}
	}

	logVerbose("Discovered MQTT register: %s", md.Name)

	regCtx, regCancel := context.WithCancel(ctx)
	restMetadata := mqttMetadataToREST(md.Metadata)

	// Consume from MQTT (using string channels)
	mqttReaderStr, mqttWriterStr := reg.ConsumeString(mqttRegs, md.Name)

	// Convert string channels to byte channels
	mqttReader := make(chan []byte, 1)
	mqttWriter := make(chan []byte, 1)

	// Goroutine to convert string→bytes for reading
	go func() {
		for str := range mqttReaderStr {
			mqttReader <- []byte(str)
		}
		close(mqttReader)
	}()

	// Goroutine to convert bytes→string for writing
	go func() {
		for b := range mqttWriter {
			mqttWriterStr <- string(b)
		}
		close(mqttWriterStr)
	}()

	sync := &registerSync{
		name:          md.Name,
		mqttMetadata:  md.Metadata,
		mqttProvider:  mqttWriter,
		mqttConsumer:  mqttReader,
		restMetadata:  restMetadata,
		lastMQTTValue: nil,
		lastRESTValue: nil,
		cancel:        regCancel,
	}

	syncMu.Lock()
	registerSyncs[md.Name] = sync
	syncMu.Unlock()

	// Start polling for REST change requests → send to MQTT
	go pollRESTChangeRequests(regCtx, providerClient, md.Name, mqttWriter)

	logVerbose("Started bidirectional sync for %s", md.Name)
}

func handleMQTTValueBatched(
	val reg.NameAndValue,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
	batchMgr *batchManager,
) {
	syncMu.Lock()
	sync, exists := registerSyncs[val.Name]
	syncMu.Unlock()

	if !exists {
		return
	}

	// Check if we should propagate (loop prevention)
	if !shouldPropagate(sync, val.Value, true) {
		return
	}

	// Convert value to REST format
	restValue := mqttValueToREST(val.Value)

	// Add to batch instead of sending immediately
	batchMgr.mu.Lock()
	batchMgr.pending[val.Name] = &batchEntry{
		value:    restValue,
		metadata: sync.restMetadata,
		ttl:      batchMgr.defaultTTL,
	}
	batchMgr.mu.Unlock()

	logVerbose("Queued %s for batch update", val.Name)
}

func handleRESTUpdate(
	ctx context.Context,
	update restRegisterUpdate,
	mqttRegs *reg.Registers,
	providerClient *rest.ProviderClient,
	consumerClient *rest.ConsumerClient,
	ttl time.Duration,
	includePatterns, excludePatterns []string,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	// Check if register should be synced
	if !shouldSyncRegister(update.Name, includePatterns, excludePatterns) {
		return
	}

	syncMu.Lock()
	existingSync, exists := registerSyncs[update.Name]
	syncMu.Unlock()

	if exists {
		// Phase 4: Check for metadata update
		if !metadataEqual(existingSync.restMetadata, update.Metadata) {
			logVerbose("Metadata changed for REST register: %s", update.Name)
			// Cancel existing sync and recreate
			existingSync.cancel()
			syncMu.Lock()
			delete(registerSyncs, update.Name)
			syncMu.Unlock()
			// Fall through to recreate
		} else {
			// Just a value update
			handleRESTValueUpdate(existingSync, update)
			return
		}
	}

	// New REST register - create MQTT provider
	logVerbose("Discovered REST register: %s", update.Name)

	regCtx, regCancel := context.WithCancel(ctx)
	mqttMetadata := restMetadataToMQTT(update.Metadata)

	// Create MQTT provider (using string channels)
	mqttReaderStr, mqttWriterStr := reg.ProvideString(mqttRegs, update.Name, mqttMetadata)

	// Convert string channels to byte channels
	mqttReader := make(chan []byte, 1)
	mqttWriter := make(chan []byte, 1)

	// Goroutine to convert string→bytes for reading
	go func() {
		for str := range mqttReaderStr {
			mqttReader <- []byte(str)
		}
		close(mqttReader)
	}()

	// Goroutine to convert bytes→string for writing
	go func() {
		for b := range mqttWriter {
			mqttWriterStr <- string(b)
		}
		close(mqttWriterStr)
	}()

	newSync := &registerSync{
		name:          update.Name,
		mqttMetadata:  mqttMetadata,
		mqttProvider:  mqttWriter,
		mqttConsumer:  mqttReader,
		restMetadata:  update.Metadata,
		lastMQTTValue: nil,
		lastRESTValue: nil,
		cancel:        regCancel,
	}

	syncMu.Lock()
	registerSyncs[update.Name] = newSync
	syncMu.Unlock()

	// Send initial value to MQTT
	restValueBytes := restValueToMQTT(update.Value)
	if shouldPropagate(newSync, restValueBytes, false) {
		mqttWriter <- restValueBytes
		recordPropagation(newSync, restValueBytes, false)
		logVerbose("Synced %s to MQTT: %v", update.Name, update.Value)
	}

	// Handle MQTT set requests → REST change requests
	go handleMQTTSetRequests(regCtx, consumerClient, update.Name, mqttReader)

	logVerbose("Started bidirectional sync for %s (from REST)", update.Name)
}

func handleRESTValueUpdate(sync *registerSync, update restRegisterUpdate) {
	restValueBytes := restValueToMQTT(update.Value)

	if !shouldPropagate(sync, restValueBytes, false) {
		return
	}

	select {
	case sync.mqttProvider <- restValueBytes:
		recordPropagation(sync, restValueBytes, false)
		logVerbose("Synced %s to MQTT: %v", update.Name, update.Value)
	default:
		log.Printf("Warning: failed to sync %s to MQTT, channel busy", update.Name)
	}
}

// pollRESTChangeRequests polls for consumer change requests from REST and forwards to MQTT
// This is used when MQTT provides a register to REST, and REST consumers can request changes
func pollRESTChangeRequests(ctx context.Context, providerClient *rest.ProviderClient, name string, mqttWriter chan<- []byte) {
	pollInterval := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Poll for change requests from REST consumers
		requests, err := providerClient.GetChangeRequests(ctx, []string{name}, pollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logVerbose("Error polling change requests for %s: %v", name, err)
			time.Sleep(1 * time.Second)
			continue
		}

		// Forward change request to MQTT
		if value, exists := requests[name]; exists {
			reqBytes := restValueToMQTT(value)
			logVerbose("REST change request for %s: %v", name, value)

			select {
			case mqttWriter <- reqBytes:
				logVerbose("Forwarded change request to MQTT for %s", name)
			case <-ctx.Done():
				return
			default:
				log.Printf("Warning: failed to forward change request for %s", name)
			}
		}
	}
}

// handleMQTTSetRequests forwards MQTT set requests to REST as consumer change requests
func handleMQTTSetRequests(ctx context.Context, consumerClient *rest.ConsumerClient, name string, mqttReader <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case setReq, ok := <-mqttReader:
			if !ok {
				return
			}

			restValue := mqttValueToREST(setReq)
			logVerbose("MQTT set request for %s: %v", name, restValue)

			err := consumerClient.RequestChange(ctx, name, restValue)
			if err != nil {
				log.Printf("Warning: failed to forward set request for %s: %v", name, err)
			} else {
				logVerbose("Forwarded set request to REST for %s", name)
			}
		}
	}
}

func mqttMetadataToREST(mqtt reg.Metadata) map[string]any {
	rest := make(map[string]any)
	for k, v := range mqtt {
		rest[k] = v
	}
	return rest
}

func restMetadataToMQTT(rest map[string]any) reg.Metadata {
	mqtt := make(reg.Metadata)
	for k, v := range rest {
		// Convert any to string (lossy for complex types)
		switch val := v.(type) {
		case string:
			mqtt[k] = val
		default:
			// Serialize complex values as JSON strings
			bytes, _ := json.Marshal(val)
			mqtt[k] = string(bytes)
		}
	}
	return mqtt
}

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
