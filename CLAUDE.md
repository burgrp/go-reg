# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go-based CLI tool and library implementing the "Register R/W over MQTT" protocol. It models a distributed key/value map over MQTT where each key/value pair is called a "register". The project provides both a library for consuming/providing registers and a CLI tool for manual manipulation.

## Building and Running

### Local Development Build
```bash
go mod tidy
go build -o mreg
```

### Release Build
The project uses `build.sh` for cross-platform releases (triggered by GitHub Actions on version tags). The build script:
- Checks for version tags matching `refs/tags/v*`
- Extracts version from git tag
- Builds for multiple platforms: darwin/amd64, darwin/arm64, linux/amd64, linux/arm, linux/arm64, windows/amd64, windows/arm, windows/arm64
- Injects version into binary using ldflags: `-X 'github.com/burgrp/go-reg/cmd.Version=$VERSION'`
- Publishes binaries to GitHub releases using `ghr`

### Running the CLI
```bash
# Set MQTT broker (required for all commands)
export MQTT=hostname:port  # or just hostname (defaults to port 1883)

# Read a register
./mreg get <register-name>

# Write a register
./mreg set <register-name> <json-value>

# List all registers
./mreg list

# Provide a virtual register
./mreg provide <name> '{"device":"My Device"}' <initial-json-value>
```

## Architecture

### Core Package: `reg/`

**`reg/registers.go`** - Main register implementation with MQTT client management
- `NewRegisters()` - Creates MQTT connection using `MQTT` environment variable
- `Consume()` - Generic register consumer (with type parameters for string/number/bool variants)
- `Provide()` - Generic register provider (with type parameters for string/number/bool variants)
- `Watch()` - Monitors all registers for metadata and value changes

**`reg/serialize.go`** - Type serialization for MQTT payloads
- Provides `Serialize[T]` and `Deserialize[T]` function types
- Built-in serializers for string, number (float64), and bool types
- JSON values are handled as byte arrays directly in most CLI commands

### CLI Package: `cmd/`

**`cmd/root.go`** - Cobra root command setup
- Defines base CLI structure
- Enforces `MQTT` environment variable requirement

**`cmd/common.go`** - Shared JSON serialization utilities used by CLI commands

**`cmd/version.go`** - Version command
- `Version` variable is set at build time via ldflags

**Command implementations:** `cmd/get.go`, `cmd/set.go`, `cmd/list.go`, `cmd/provide.go`
- Each command follows the MQTT register protocol defined in README
- Support for `--stay` flag to maintain connection and stream changes

### MQTT Topic Structure

The protocol uses these topic patterns (see README for full protocol):
- `register/<name>/get` - Request current value
- `register/<name>/set` - Write new value
- `register/<name>/is` - Publish current value
- `register/<name>/advertise` - Publish register metadata
- `register/advertise!` - Broadcast challenge for all registers to advertise

### Version Management

The version is injected at build time into `cmd.Version` variable. Default value is "local-build" for non-release builds.

## Package Structure

- `main.go` - Entry point, delegates to `cmd.Execute()`
- `reg/` - Core library for register operations
- `cmd/` - CLI commands built with Cobra framework
- `examples/` - Example programs demonstrating library usage
- `scripts/update-doc/` - Documentation generation tool

## Key Dependencies

- `github.com/eclipse/paho.mqtt.golang` - MQTT client
- `github.com/spf13/cobra` - CLI framework
