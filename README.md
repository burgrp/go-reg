# mqtt-reg
Register R/W over MQTT

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


