package goreg

import (
	"errors"
	"os"
	"strings"
	"time"

	"encoding/json"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Registers struct {
	mqtt mqtt.Client
}

type Metadata map[string]string

// NewRegisters creates a new Registers object.
// It will connect to the MQTT broker specified in the MQTT environment variable.
// If the variable is not set, an error is returned.
func NewRegisters() (*Registers, error) {

	registers := &Registers{}

	broker := os.Getenv("MQTT")
	if broker == "" {
		return nil, errors.New("MQTT environment variable not set")
	}

	opts := mqtt.NewClientOptions()
	if !strings.Contains(broker, ":") {
		broker += ":1883"
	}
	opts.AddBroker("tcp://" + broker)
	opts.AutoReconnect = true
	opts.ConnectRetryInterval = 10 * time.Second
	opts.ConnectRetry = true

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	registers.mqtt = client

	return registers, nil
}

// Consume creates a new register client with the given name.
func Consume[T comparable](registers *Registers, name string, serialize Serialize[T], deserialize Deserialize[T]) (<-chan T, chan<- T) {
	readerPrimary := make(chan []byte, 1)
	readerSecondary := make(chan T, 1)
	foreignGet := make(chan struct{})
	writer := make(chan T)

	var NULL T
	var value = NULL

	go func() {
		for {
			if registers.mqtt.IsConnectionOpen() {
				registers.mqtt.Publish(format_topic(name, "get"), 0, false, []byte{})
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	registers.mqtt.Subscribe(format_topic(name, "is"), 0, func(client mqtt.Client, msg mqtt.Message) {
		v := msg.Payload()
		select {
		case readerPrimary <- v:
		default:
			// buffer full, overwrite old value
			<-readerPrimary
			readerPrimary <- v
		}
	})

	registers.mqtt.Subscribe(format_topic(name, "get"), 0, func(client mqtt.Client, msg mqtt.Message) {
		select {
		case foreignGet <- struct{}{}:
		default: // ignore
		}
	})

	go func() {
		for v := range writer {
			registers.mqtt.Publish(format_topic(name, "set"), 0, false, serialize(v))
		}
	}()

	go func() {
		for {
			var serialized_value []byte

			waitAgain := false
			select {
			case v := <-readerPrimary:
				serialized_value = v
			case <-foreignGet:
				waitAgain = true
			case <-time.After(5 * time.Second):
				registers.mqtt.Publish(format_topic(name, "get"), 0, false, []byte{})
				waitAgain = true
			}

			if waitAgain {
				select {
				case v := <-readerPrimary:
					serialized_value = v
				case <-time.After(5 * time.Second):
				}
			}

			v := deserialize(serialized_value)
			if v != value {
				value = v

				select {
				case readerSecondary <- value:
				default:
					// buffer full, overwrite old value
					<-readerSecondary
					readerSecondary <- value
				}
			}

		}
	}()

	return readerSecondary, writer
}

func ConsumeString(registers *Registers, name string) (<-chan string, chan<- string) {
	return Consume(registers, name, StringSerialize, StringDeserialize)
}

func ConsumeNumber(registers *Registers, name string) (<-chan float64, chan<- float64) {
	return Consume(registers, name, NumberSerialize, NumberDeserialize)
}

func ConsumeBool(registers *Registers, name string) (<-chan bool, chan<- bool) {
	return Consume(registers, name, BoolSerialize, BoolDeserialize)
}

// Provide creates a new register server with the given name.
func Provide[T comparable](registers *Registers, name string, serialize Serialize[T], deserialize Deserialize[T], metadata Metadata) (<-chan T, chan<- T) {
	reader := make(chan T)
	writer := make(chan T)
	publish := make(chan T)
	advertise := make(chan bool)

	var value T

	json_metadata, err := json.Marshal(metadata)
	if err != nil {
		json_metadata = []byte("{}")
	}

	go func() {
		for range advertise {
			registers.mqtt.Publish(format_topic(name, "advertise"), 0, false, json_metadata)
		}
	}()

	go func() {
		for {
			if registers.mqtt.IsConnectionOpen() {
				advertise <- true
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	go func() {
		for v := range publish {
			registers.mqtt.Publish(format_topic(name, "is"), 0, false, serialize(v))
		}
	}()

	registers.mqtt.Subscribe("register/advertise!", 0, func(client mqtt.Client, msg mqtt.Message) {
		advertise <- true
	})

	registers.mqtt.Subscribe(format_topic(name, "get"), 0, func(client mqtt.Client, msg mqtt.Message) {
		publish <- value
	})

	registers.mqtt.Subscribe(format_topic(name, "set"), 0, func(client mqtt.Client, msg mqtt.Message) {
		v := deserialize(msg.Payload())
		select {
		case reader <- v:
		default:
			// buffer full, overwrite old value
			<-reader
			reader <- v
		}
	})

	go func() {
		for v := range writer {
			if v != value {
				value = v
				publish <- v
				select {
				case reader <- value:
				default:
					// buffer full, overwrite old value
					<-reader
					reader <- value
				}
			}
		}
	}()

	return reader, writer
}

func ProvideString(registers *Registers, name string, metadata Metadata) (<-chan string, chan<- string) {
	return Provide(registers, name, StringSerialize, StringDeserialize, metadata)
}

func ProvideNumber(registers *Registers, name string, metadata Metadata) (<-chan float64, chan<- float64) {
	return Provide(registers, name, NumberSerialize, NumberDeserialize, metadata)
}

func ProvideBool(registers *Registers, name string, metadata Metadata) (<-chan bool, chan<- bool) {
	return Provide(registers, name, BoolSerialize, BoolDeserialize, metadata)
}

type NameAndMetadata struct {
	Name     string
	Metadata Metadata
}

type NameAndValue struct {
	Name  string
	Value []byte
}

// Watch watches for new registers and value changes.
func Watch(registers *Registers) (<-chan NameAndMetadata, <-chan NameAndValue) {
	values := make(chan NameAndValue)
	metadata := make(chan NameAndMetadata)

	go func() {
		for {
			if registers.mqtt.IsConnectionOpen() {
				registers.mqtt.Publish("register/advertise!", 0, false, []byte{})
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	registers.mqtt.Subscribe("register/+/advertise", 0, func(client mqtt.Client, msg mqtt.Message) {
		parsed_topic := strings.Split(msg.Topic(), "/")
		name := parsed_topic[1]
		var md Metadata
		json.Unmarshal(msg.Payload(), &md)
		metadata <- NameAndMetadata{name, md}
	})

	registers.mqtt.Subscribe("register/+/is", 0, func(client mqtt.Client, msg mqtt.Message) {
		parsed_topic := strings.Split(msg.Topic(), "/")
		values <- NameAndValue{parsed_topic[1], msg.Payload()}
	})

	return metadata, values
}

func (registers *Registers) SetValue(name string, value []byte) {
	registers.mqtt.Publish(format_topic(name, "set"), 0, false, value)
}

func format_topic(name string, suffix string) string {
	return "register/" + name + "/" + suffix
}
