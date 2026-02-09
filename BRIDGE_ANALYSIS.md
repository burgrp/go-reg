# Bridge Feasibility Analysis: MQTT ↔ REST Registry

## Protocol Comparison

### MQTT Protocol (Current - mreg)
- **Architecture**: Distributed, broker-based, no central server
- **Transport**: MQTT pub/sub topics
- **Value Types**: `[]byte` (raw JSON)
- **Metadata**: `map[string]string`
- **Change Mechanism**: `set` topic (fire and forget)
- **Discovery**: `advertise!` challenge + `advertise` responses
- **TTL**: ❌ No concept of expiration
- **Auto-reconnect**: ✅ Built into MQTT client

**API:**
```go
Watch() // Discover all registers + value changes
Consume(name) // (<-chan T, chan<- T) - read values, send sets
Provide(name) // (<-chan T, chan<- T) - publish values, receive sets
```

### REST Registry Protocol (New - reg)
- **Architecture**: Centralized server with REST API
- **Transport**: HTTP with long-polling
- **Value Types**: `any` (JSON-compatible)
- **Metadata**: `map[string]any`
- **Change Mechanism**: Change request queue (provider can accept/reject)
- **Discovery**: `ConsumeAll()` lists all registers
- **TTL**: ✅ **Required** - registers expire if not refreshed
- **Auto-refresh**: ✅ Client auto-refreshes providers

**API:**
```go
ConsumeAll(ctx) // Discover all registers + updates
Consume(ctx, name) // (<-chan ValueAndMetadata, chan<- any, error)
Provide(ctx, name, value, metadata, ttl) // (chan<- any, <-chan any, error)
```

## Compatibility Matrix

| Aspect | MQTT | REST | Bridge Impact |
|--------|------|------|---------------|
| **Value Format** | `[]byte` JSON | `any` JSON | ✅ Easy conversion via JSON |
| **Metadata** | `map[string]string` | `map[string]any` | ✅ String→any trivial, any→string lossy |
| **Discovery** | Watch() | ConsumeAll() | ✅ Both support full discovery |
| **Change Requests** | set topic | Change queue | ✅ Semantically equivalent |
| **Bidirectional** | Yes (set/is) | Yes (request/update) | ✅ Both support bidirectional |
| **TTL** | No | Required | ⚠️ **Need default TTL for MQTT registers** |
| **Reconnection** | Auto | Auto | ✅ Both handle it |

## Bridge Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                         Bridge Service                            │
│  (Single process, connects to both MQTT broker and REST server)  │
│                                                                    │
│  ┌──────────────────────────┐  ┌──────────────────────────┐     │
│  │   MQTT → REST Sync       │  │   REST → MQTT Sync       │     │
│  ├──────────────────────────┤  ├──────────────────────────┤     │
│  │                          │  │                          │     │
│  │ 1. Watch() all MQTT regs │  │ 1. ConsumeAll() REST     │     │
│  │ 2. For each register:    │  │ 2. For each register:    │     │
│  │    • Act as REST Provider│  │    • Act as MQTT Provider│     │
│  │    • Propagate values    │  │    • Propagate values    │     │
│  │    • Poll change requests│  │    • Listen for sets     │     │
│  │    • Send to MQTT        │  │    • Send to REST        │     │
│  │                          │  │                          │     │
│  └──────────────────────────┘  └──────────────────────────┘     │
│                                                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │              Register State Tracker                       │   │
│  │  • Map of register name → SyncState                       │   │
│  │  • Track last propagated value per direction              │   │
│  │  • Detect actual changes (avoid propagating same value)   │   │
│  │  • Debounce rapid changes                                 │   │
│  └──────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
         ↕                                              ↕
    MQTT Broker                                  REST Registry
  (register/*/set)                            (PUT/GET endpoints)
  (register/*/is)
```

## Critical Design Challenges

### 1. Loop Prevention 🔴 **CRITICAL**

**Problem**:
```
MQTT value changes → Bridge → REST Provider → Bridge polls REST change →
Bridge → MQTT set → MQTT value changes → LOOP!
```

**Solution Strategy**:
```go
type RegisterState struct {
    Name              string
    LastMQTTValue     []byte    // Last value we propagated from MQTT
    LastRESTValue     []byte    // Last value we propagated from REST
    LastMQTTUpdate    time.Time
    LastRESTUpdate    time.Time
}

// Only propagate if:
// 1. Value actually changed compared to last propagated value
// 2. Not within debounce window (100ms)
// 3. Value originated from the other side
```

**Key Insight**: We act as BOTH provider and consumer on each side, so we can track which direction value flows and only propagate genuine changes.

### 2. TTL Management ⚠️ **IMPORTANT**

**Problem**: MQTT registers have no TTL concept, but REST requires TTL.

**Solution**:
- Bridge provides default TTL (e.g., 30s) for all MQTT registers
- REST client auto-refreshes every TTL/2 (15s)
- If MQTT register stops advertising/updating, let REST register expire naturally
- **Configuration**: `--default-ttl 30s`

### 3. Metadata Mapping ⚠️ **DATA LOSS RISK**

**Problem**:
- MQTT: `map[string]string` (device, type, unit)
- REST: `map[string]any` (arbitrary nested objects)

**Solution**:
- MQTT→REST: Easy, string values fit in `any`
- REST→MQTT: Lossy, flatten complex values to strings with JSON.Marshal
- **Accept data loss** when REST→MQTT for complex metadata

### 4. Value Type Handling ✅

**Solution**: Use JSON as intermediate format
```go
// MQTT []byte → Go value → REST any
func mqttToREST(mqttBytes []byte) any {
    var value any
    json.Unmarshal(mqttBytes, &value)
    return value
}

// REST any → Go value → MQTT []byte
func restToMQTT(restValue any) []byte {
    bytes, _ := json.Marshal(restValue)
    return bytes
}
```

### 5. Register Lifecycle ✅

**States**:
1. **Discovered**: Register seen on one side
2. **Syncing**: Active bidirectional sync
3. **Stale**: Not seen for N seconds
4. **Removed**: Cleanup goroutines

**Handling**:
- New register appears → Spawn sync goroutines
- Register disappears (MQTT) → Let REST TTL expire naturally
- Register expires (REST) → Stop propagating to MQTT

## Implementation Plan

### Phase 1: Foundation (Start Simple)
```bash
mreg bridge --mqtt localhost:1883 --registry http://localhost:8080
```

**One-way bridge: MQTT → REST only**
- Prove the concept
- No loop concerns
- Test TTL handling
- ✅ **This is completely safe and simple**

### Phase 2: Bidirectional + Loop Prevention
- Add REST → MQTT direction
- Implement state tracking
- Add debouncing
- Test loop prevention

### Phase 3: Polish
- Register filtering (`--filter "sensor.*"`)
- Metrics/logging
- Health checks
- Graceful shutdown

## Feasibility Verdict

### ✅ **YES - Definitely Doable!**

**Reasons:**
1. ✅ Both protocols have excellent Go client libraries with channel-based APIs
2. ✅ Both support the primitives needed (provide, consume, change requests)
3. ✅ Value types are compatible (both use JSON)
4. ✅ Both support dynamic register discovery
5. ✅ Both auto-reconnect on connection failures
6. ✅ Loop prevention is solvable with value tracking + debouncing
7. ✅ TTL can be provided as a fixed default for MQTT registers

**Main Complexity**: Loop prevention and state tracking, but this is manageable.

**Risk Level**: 🟢 **Low** - Both protocols are well-designed for this use case.

## Recommended Cobra Command Structure

```go
// cmd/bridge.go
var bridgeCmd = &cobra.Command{
    Use:   "bridge",
    Short: "Bridge MQTT registers with REST registry",
    Long: `Runs a bidirectional bridge that synchronizes registers between
MQTT (this protocol) and REST registry (github.com/burgrp/reg).

Registers on MQTT will appear in REST registry and vice versa.
Changes made on either side propagate to the other.`,
    RunE: runBridge,
}

func init() {
    bridgeCmd.Flags().String("registry", "", "REST registry URL (e.g., http://localhost:8080)")
    bridgeCmd.Flags().Duration("ttl", 30*time.Second, "Default TTL for MQTT registers in REST")
    bridgeCmd.Flags().String("filter", "", "Only bridge registers matching pattern")
    bridgeCmd.MarkFlagRequired("registry")
    RootCmd.AddCommand(bridgeCmd)
}
```

## Example Usage

```bash
# Terminal 1: Start REST registry server
cd /path/to/reg
./reg serve

# Terminal 2: Start MQTT broker
mosquitto

# Terminal 3: Run bridge
export MQTT=localhost:1883
mreg bridge --registry http://localhost:8080 --ttl 30s

# Terminal 4: Create MQTT register
mreg provide temp '{"device":"sensor1"}' 22.5 --stay

# Terminal 5: Read from REST registry
reg get temp
# Output: 22.5 with metadata {"device":"sensor1"}

# Terminal 6: Create REST register
reg provide humidity 60 '{"unit":"percent"}' --ttl 10s --stay

# Terminal 7: Read from MQTT
mreg get humidity
# Output: 60
```

## Next Steps

1. ✅ **Start with one-way bridge (MQTT→REST)** - Safest, proves concept
2. Add bidirectional sync with loop prevention
3. Add filtering and configuration
4. Write tests
5. Document behavior and limitations
