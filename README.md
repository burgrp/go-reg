# Register R/W over MQTT

`Register R/W over MQTT` is a simple and intuitive approach to model a distributed key/value map over the network. Each key/value pair in the distributed map is referred to as a `register`. There is no central point other than the MQTT broker. This protocol unifies communication between smart devices and allows to expose the state of these devices in the form of metrics, which can be further stored and visualized.

This repository servers for the following purposes:
- Provides Go language library to consume and provide registers.
- Provides simple CLI tool to manipulate the registers.
- Describes the communication protocol.

## Protocol

`Register R/W over MQTT` is a simple protocol based on generic MQTT. The protocol models so called "registers" where register value can be read or written by MQTT topics.

### Register names

Each register is identified by a string name. By convention the name is lowercase string without spaces. Optionally the name can be formed as a logical path separated by dots, e.g. `kitchen.temperature`, `kitchen.humidity` or `buildingA.floor0.room10.lights`

### Register values

Register value may be any valid JSON type, i.e. number, string, boolean, object or array. Null values are passed as empty (zero-length) messages. Meaning of null (or undefined on js API level) values is, that the register is unavailable.

### MQTT topics

There are two mandatory topics every device has to implement: listen for `get`, publish `is` and two optional topics: listen for `set`, publish `advertise`.

#### register/(registername)/get

message format: none

Register MUST listen for this topic and immediately send current state of the register by publishing `is` topic.

example:
```
register/kitchen.temperature/get (no message data)
```

#### register/(registername)/set

message format: any valid JSON value

This is mandatory topic for writable registers. Register MAY listen for this topic and set the register value to the decoded JSON data from message. If register listens for the topic, then it MUST publish `is` topic with the new state. In some situations the value sent in `is` topic is not exactly the value received in `set` topic - this may happen if the value to be set is out of range and the register clips the value to an acceptable value.

example:
```
register/kitchen.lights/set true
register/kitchen.display.text/set "Refrigerator door open"
register/livingroom.curtain.position/set 50
register/livingroom.lights/set [50,50,100]
```

#### register/(registername)/is

message format: any valid JSON value

Register MUST publish this topic in following situations:

- when register receives `get` topic
- when register receives `set` topic and is writable register

Message data is JSON encoded register value.

example:
```
register/kitchen.lights/is true
register/kitchen.temperature/is 24.3
```

#### register/(registername)/advertise

message format: JSON object

Register MAY publish this topic in order to let other parties know about register presence. Message sent with this topic is JSON encoded object with details about register. Register SHOULD set these properties in published data:

- `device` a device identifier, which may serve to logically group registers in a graphical UI.
- `type` one of data type identifiers: `number`, `string`, `boolean`, `array`, `object`
- `unit` name of a unit if the register represents physical property, e.g. `°C`.

#### register/advertise!

message format: none

This is a broadcast challenge for all registers to advertise. Registers MAY respond with `register/(registername)/advertise`.

## API

TBD

## CLI

### Installation

Download binary for your platform from the latest [release](https://github.com/burgrp/go-reg).

### Compilation

```sh
go mod tidy
go build -o mreg
```

### Reference

#### mreg

mreg is a command line tool for working with registers over MQTT.

##### Synopsis

The mreg command is a command line tool for working with registers over MQTT.
It allows you to read, write and list registers.
Furthermore it can provide a 'virtual' register which is convenient for debugging of consumers of the register.

MQTT broker is specified using environment variable MQTT as hostname or hostname:port.

For more information on registers over MQTT, see: https://github.com/burgrp/go-reg .

##### Options

```
  -h, --help   help for mreg
```

##### SEE ALSO

* [mreg bridge](#mreg-bridge)	 - Bridge MQTT registers to REST registry
* [mreg get](#mreg-get)	 - Read a register
* [mreg list](#mreg-list)	 - List all known registers
* [mreg provide](#mreg-provide)	 - Provide a register
* [mreg set](#mreg-set)	 - Write a register
* [mreg version](#mreg-version)	 - Show version

#### mreg bridge

Bridge MQTT registers to REST registry

##### Synopsis

Runs a one-way bridge that propagates MQTT registers to the REST registry.

MQTT registers will automatically appear in the REST registry with their values and metadata.
When MQTT register values change, they are propagated to REST in real-time.

The REGISTRY environment variable must be set to the REST registry URL.

Example:
  export MQTT=localhost:1883
  export REGISTRY=http://localhost:8080
  mreg bridge --ttl 30s

```
mreg bridge [flags]
```

##### Options

```
  -h, --help           help for bridge
      --ttl duration   Default TTL for MQTT registers in REST registry (default 10s)
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg get

Read a register

##### Synopsis

Reads the specified register.
With --stay flag, the command will remain connected and write any changes to stdout.

```
mreg get <register> [flags]
```

##### Options

```
  -h, --help   help for get
  -s, --stay   Stay connected and write changes to stdout
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg help

Help about any command

##### Synopsis

Help provides help for any command in the application.
Simply type mreg help [path to command] for full details.

```
mreg help [command] [flags]
```

##### Options

```
  -h, --help   help for help
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg list

List all known registers

##### Synopsis

Lists all known registers by sending advertise challenge to all devices.
With --stay flag, the command will remain connected and write any changes to stdout.
If registers are specified, only those will be listed.

```
mreg list [<reg1> <reg2> ...] [flags]
```

##### Options

```
  -h, --help               help for list
  -s, --stay               Stay connected, write changes to stdout
  -t, --timeout duration   Timeout for waiting for advertise challenge to be answered (default 5s)
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg provide

Provide a register

##### Synopsis

Registers are established by advertising them to the network.
The 'meta' argument is a JSON object containing metadata for the register, such as {"device": "My device"}.
You can also specify an initial value as the last argument. If no initial value is provided, the register will be created with a null value.
Values for registers are defined using JSON expressions, such as true, false, 3.14, "hello world," or null.
Additionally, subsequent values can be read from stdin and written to stdout.

```
mreg provide <name> <meta> [<value>] [flags]
```

##### Options

```
  -h, --help        help for provide
  -r, --read-only   Make the register read-only.
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg set

Write a register

##### Synopsis

Writes the specified register.
With --stay flag, the command will remain connected, read values from stdin and write any changes to stdout.
Values are specified as JSON expressions, e.g. true, false, 3.14, "hello world" or null.

```
mreg set <register> <value> [flags]
```

##### Options

```
  -h, --help               help for set
  -s, --stay               Stay connected, read values from stdin and write changes to stdout
  -t, --timeout duration   Timeout for waiting for the register to be set (default 5s)
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

#### mreg version

Show version

##### Synopsis

Shows version of reg command line tool.

```
mreg version [flags]
```

##### Options

```
  -h, --help   help for version
```

##### SEE ALSO

* [mreg](#mreg)	 - mreg is a command line tool for working with registers over MQTT.

