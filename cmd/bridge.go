package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/burgrp/go-reg/reg"
	"github.com/burgrp/reg/pkg/client"
	clientfactory "github.com/burgrp/reg/pkg/client/factory"
	"github.com/spf13/cobra"
)

var bridgeCmd = &cobra.Command{
	Use:   "bridge",
	Short: "Bridge MQTT registers to REST registry",
	Long: `Runs a bidirectional bridge that synchronizes registers between MQTT and REST registry.

Registers on MQTT will appear in REST registry and vice versa.
Value changes on either side propagate to the other in real-time.
Change requests work bidirectionally with loop prevention.

The REGISTRY environment variable must be set to the REST registry URL.

Example:
  export MQTT=localhost:1883
  export REGISTRY=http://localhost:8080
  mreg bridge --ttl 30s`,
	RunE: runBridge,
}

func init() {
	RootCmd.AddCommand(bridgeCmd)
	bridgeCmd.Flags().Duration("ttl", 10*time.Second, "Default TTL for registers in both registries")
}

type registerSync struct {
	name string

	// MQTT side
	mqttMetadata reg.Metadata
	mqttProvider chan<- []byte // Write to MQTT
	mqttConsumer <-chan []byte // Read from MQTT

	// REST side
	restUpdates  chan<- any                     // Write to REST
	restConsumer <-chan client.ValueAndMetadata // Read from REST

	// Loop prevention
	lastMQTTValue  []byte    // Track last value from MQTT
	lastRESTValue  []byte    // Track last value from REST
	lastPropagated time.Time // Debouncing timestamp
	mu             sync.Mutex

	cancel context.CancelFunc
}

func runBridge(cmd *cobra.Command, args []string) error {
	// 1. Get TTL flag
	ttl, err := cmd.Flags().GetDuration("ttl")
	if err != nil {
		return err
	}

	// 2. Check REGISTRY env var
	if os.Getenv("REGISTRY") == "" {
		return fmt.Errorf("REGISTRY environment variable not set")
	}

	log.Println("Starting bidirectional MQTT↔REST bridge...")
	log.Printf("MQTT broker: %s", os.Getenv("MQTT"))
	log.Printf("REST registry: %s", os.Getenv("REGISTRY"))
	log.Printf("Default TTL: %s", ttl)

	// 3. Connect to MQTT
	mqttRegs, err := reg.NewRegisters()
	if err != nil {
		return fmt.Errorf("failed to connect to MQTT: %w", err)
	}
	log.Println("Connected to MQTT broker")

	// 4. Connect to REST
	restClient, err := clientfactory.NewClientFromEnv()
	if err != nil {
		return fmt.Errorf("failed to connect to REST registry: %w", err)
	}
	log.Println("Connected to REST registry")

	// 5. Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down bridge...")
		cancel()
	}()

	// 6. Watch MQTT registers
	mqttMetadata, mqttValues := reg.Watch(mqttRegs)
	log.Println("Watching MQTT registers...")

	// 7. Watch REST registers
	restUpdates, err := restClient.ConsumeAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to consume REST registers: %w", err)
	}
	log.Println("Watching REST registers...")

	// 8. Track active syncs
	registerSyncs := make(map[string]*registerSync)
	var syncMu sync.Mutex

	// 9. Main loop
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
			handleMQTTMetadata(ctx, md, restClient, mqttRegs, ttl, registerSyncs, &syncMu)

		case val, ok := <-mqttValues:
			if !ok {
				log.Println("MQTT values channel closed")
				return nil
			}
			handleMQTTValue(val, registerSyncs, &syncMu)

		case restUpdate, ok := <-restUpdates:
			if !ok {
				log.Println("REST updates channel closed")
				return nil
			}
			handleRESTUpdate(ctx, restUpdate, mqttRegs, restClient, ttl, registerSyncs, &syncMu)
		}
	}
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
	restClient client.Client,
	mqttRegs *reg.Registers,
	ttl time.Duration,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	syncMu.Lock()
	if _, exists := registerSyncs[md.Name]; exists {
		syncMu.Unlock()
		return
	}
	syncMu.Unlock()

	log.Printf("Discovered MQTT register: %s", md.Name)

	regCtx, regCancel := context.WithCancel(ctx)
	restMetadata := mqttMetadataToREST(md.Metadata)

	// Create REST provider
	restUpdates, restChangeRequests, err := restClient.Provide(regCtx, md.Name, nil, restMetadata, ttl)
	if err != nil {
		log.Printf("Error creating REST provider for %s: %v", md.Name, err)
		regCancel()
		return
	}

	// Also consume from MQTT (using string channels)
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
		restUpdates:   restUpdates,
		lastMQTTValue: nil,
		lastRESTValue: nil,
		cancel:        regCancel,
	}

	syncMu.Lock()
	registerSyncs[md.Name] = sync
	syncMu.Unlock()

	// Handle REST change requests → send to MQTT
	go handleRESTChangeRequests(sync, restChangeRequests)

	log.Printf("Started bidirectional sync for %s", md.Name)
}

func handleMQTTValue(
	val reg.NameAndValue,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
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

	restValue := mqttValueToREST(val.Value)

	select {
	case sync.restUpdates <- restValue:
		recordPropagation(sync, val.Value, true)
		log.Printf("Synced %s to REST: %v", val.Name, restValue)
	default:
		log.Printf("Warning: failed to sync %s, channel busy", val.Name)
	}
}

func handleRESTUpdate(
	ctx context.Context,
	update client.RegisterUpdate,
	mqttRegs *reg.Registers,
	restClient client.Client,
	ttl time.Duration,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	syncMu.Lock()
	sync, exists := registerSyncs[update.Name]
	syncMu.Unlock()

	if exists {
		// Already syncing, handle value update
		handleRESTValueUpdate(sync, update)
		return
	}

	// New REST register - create MQTT provider
	log.Printf("Discovered REST register: %s", update.Name)

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

	// Create REST consumer
	restValues, restRequests, err := restClient.Consume(regCtx, update.Name)
	if err != nil {
		log.Printf("Error consuming REST register %s: %v", update.Name, err)
		regCancel()
		return
	}

	newSync := &registerSync{
		name:          update.Name,
		mqttMetadata:  mqttMetadata,
		mqttProvider:  mqttWriter,
		mqttConsumer:  mqttReader,
		restConsumer:  restValues,
		restUpdates:   nil, // No REST provider for this one
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
		log.Printf("Synced %s to MQTT: %v", update.Name, update.Value)
	}

	// Handle REST value updates → MQTT
	go handleRESTToMQTTSync(newSync, restValues)

	// Handle MQTT set requests → REST change requests
	go handleMQTTSetRequests(newSync, mqttReader, restRequests)

	log.Printf("Started bidirectional sync for %s (from REST)", update.Name)
}

func handleRESTValueUpdate(sync *registerSync, update client.RegisterUpdate) {
	restValueBytes := restValueToMQTT(update.Value)

	if !shouldPropagate(sync, restValueBytes, false) {
		return
	}

	select {
	case sync.mqttProvider <- restValueBytes:
		recordPropagation(sync, restValueBytes, false)
		log.Printf("Synced %s to MQTT: %v", update.Name, update.Value)
	default:
		log.Printf("Warning: failed to sync %s to MQTT, channel busy", update.Name)
	}
}

func handleRESTChangeRequests(sync *registerSync, changeRequests <-chan any) {
	for req := range changeRequests {
		reqBytes := restValueToMQTT(req)
		log.Printf("REST change request for %s: %v", sync.name, req)

		select {
		case sync.mqttProvider <- reqBytes:
			log.Printf("Forwarded change request to MQTT for %s", sync.name)
		default:
			log.Printf("Warning: failed to forward change request for %s", sync.name)
		}
	}
}

func handleRESTToMQTTSync(sync *registerSync, restValues <-chan client.ValueAndMetadata) {
	for update := range restValues {
		restValueBytes := restValueToMQTT(update.Value)

		if !shouldPropagate(sync, restValueBytes, false) {
			continue
		}

		select {
		case sync.mqttProvider <- restValueBytes:
			recordPropagation(sync, restValueBytes, false)
			log.Printf("Synced %s to MQTT: %v", sync.name, update.Value)
		default:
			log.Printf("Warning: failed to sync %s to MQTT, channel busy", sync.name)
		}
	}
}

func handleMQTTSetRequests(sync *registerSync, mqttReader <-chan []byte, restRequests chan<- any) {
	for setReq := range mqttReader {
		restValue := mqttValueToREST(setReq)
		log.Printf("MQTT set request for %s: %v", sync.name, restValue)

		select {
		case restRequests <- restValue:
			log.Printf("Forwarded set request to REST for %s", sync.name)
		default:
			log.Printf("Warning: failed to forward set request for %s", sync.name)
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
