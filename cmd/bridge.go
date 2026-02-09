package cmd

import (
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
	Long: `Runs a one-way bridge that propagates MQTT registers to the REST registry.

MQTT registers will automatically appear in the REST registry with their values and metadata.
When MQTT register values change, they are propagated to REST in real-time.

The REGISTRY environment variable must be set to the REST registry URL.

Example:
  export MQTT=localhost:1883
  export REGISTRY=http://localhost:8080
  mreg bridge --ttl 30s`,
	RunE: runBridge,
}

func init() {
	RootCmd.AddCommand(bridgeCmd)
	bridgeCmd.Flags().Duration("ttl", 10*time.Second, "Default TTL for MQTT registers in REST registry")
}

type registerSync struct {
	name         string
	mqttMetadata reg.Metadata
	restUpdates  chan<- any
	cancel       context.CancelFunc
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

	log.Println("Starting MQTT→REST bridge...")
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
	metadata, values := reg.Watch(mqttRegs)
	log.Println("Watching MQTT registers...")

	// 7. Track active syncs
	registerSyncs := make(map[string]*registerSync)
	var syncMu sync.Mutex

	// 8. Main loop
	for {
		select {
		case <-ctx.Done():
			log.Println("Bridge stopped")
			return nil

		case md, ok := <-metadata:
			if !ok {
				log.Println("Metadata channel closed")
				return nil
			}
			handleMetadata(ctx, md, restClient, ttl, registerSyncs, &syncMu)

		case val, ok := <-values:
			if !ok {
				log.Println("Values channel closed")
				return nil
			}
			handleValue(val, registerSyncs, &syncMu)
		}
	}
}

func handleMetadata(
	ctx context.Context,
	md reg.NameAndMetadata,
	restClient client.Client,
	ttl time.Duration,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	syncMu.Lock()
	defer syncMu.Unlock()

	// Check if already tracked
	if _, exists := registerSyncs[md.Name]; exists {
		return
	}

	log.Printf("Discovered MQTT register: %s (metadata: %v)", md.Name, md.Metadata)

	// Create context for this register
	regCtx, regCancel := context.WithCancel(ctx)

	// Convert metadata
	restMetadata := mqttMetadataToREST(md.Metadata)

	// Create REST provider with nil initial value
	updates, changeRequests, err := restClient.Provide(regCtx, md.Name, nil, restMetadata, ttl)
	if err != nil {
		log.Printf("Error creating REST provider for %s: %v", md.Name, err)
		regCancel()
		return
	}

	// Ignore change requests in Phase 1 (one-way bridge)
	go func() {
		for range changeRequests {
			// Discard change requests
		}
	}()

	// Store sync info
	registerSyncs[md.Name] = &registerSync{
		name:         md.Name,
		mqttMetadata: md.Metadata,
		restUpdates:  updates,
		cancel:       regCancel,
	}

	log.Printf("Started syncing %s to REST registry", md.Name)
}

func handleValue(
	val reg.NameAndValue,
	registerSyncs map[string]*registerSync,
	syncMu *sync.Mutex,
) {
	syncMu.Lock()
	sync, exists := registerSyncs[val.Name]
	syncMu.Unlock()

	if !exists {
		// Register not yet tracked, value will be synced after metadata arrives
		return
	}

	// Convert value
	restValue := mqttValueToREST(val.Value)

	// Send to REST
	select {
	case sync.restUpdates <- restValue:
		log.Printf("Synced %s to REST: %v", val.Name, restValue)
	default:
		// Channel full or closed, log but don't block
		log.Printf("Warning: failed to sync %s, channel busy", val.Name)
	}
}

func mqttMetadataToREST(mqtt reg.Metadata) map[string]any {
	rest := make(map[string]any)
	for k, v := range mqtt {
		rest[k] = v
	}
	return rest
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
