package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"goreg/pkg/goreg"
	"sync"

	surp "github.com/burgrp/surp-go/pkg"
	"github.com/spf13/cobra"
)

var surpCmd = &cobra.Command{
	Use:   "surp",
	Short: "Starts the SURP bridge",
	Long: `Starts the bridge between MQTT and the SURP protocol.
Three environment variables are required:
- MQTT: The MQTT broker to connect to
- SURP_IF: The network interface to bind to
- SURP_GROUP: The SURP group name to join
	`,
	RunE: runSurp,
}

func init() {
	RootCmd.AddCommand(surpCmd)
}

func runSurp(cmd *cobra.Command, args []string) error {
	_, err := NewBridge(cmd.Context())
	if err != nil {
		return err
	}

	<-cmd.Context().Done()

	return nil
}

type MqttReg struct {
	name             string
	metadata         goreg.Metadata
	surpType         string
	surpSyncListener func()
	value            any
	registers        *goreg.Registers
}

type SurpReg struct {
	name        string
	setListener func(surp.Optional[[]byte])
	writer      chan<- any
	surpType    string
	value       any
}

type Bridge struct {
	context context.Context

	mqttRegs      map[string]*MqttReg
	mqttRegsMutex *sync.Mutex

	surpRegs  map[string]*SurpReg
	surpMutex *sync.Mutex

	surpGroup *surp.RegisterGroup
	registers *goreg.Registers
}

func NewBridge(ctx context.Context) (*Bridge, error) {
	bridge := &Bridge{
		context:       ctx,
		mqttRegs:      make(map[string]*MqttReg),
		mqttRegsMutex: &sync.Mutex{},
		surpRegs:      make(map[string]*SurpReg),
		surpMutex:     &sync.Mutex{},
	}

	registers, err := goreg.NewRegisters()
	if err != nil {
		return nil, err
	}
	bridge.registers = registers
	metadata, values := goreg.Watch(registers)

	env, err := surp.GetEnvironment()
	if err != nil {
		return nil, err
	}

	group, err := surp.JoinGroup(env.Interface, env.Group)
	if err != nil {
		return nil, err
	}

	bridge.surpGroup = group

	group.OnSync(bridge.handleSurpSync)

	go func() {
		for {
			select {
			case metadata := <-metadata:
				err := bridge.handleMqttMetadata(metadata.Name, metadata.Metadata)
				if err != nil {
					println("Error handling MQTT metadata: ", err.Error())
				}
			case value := <-values:
				err := bridge.handleMqttSync(value.Name, value.Value)
				if err != nil {
					println("Error handling MQTT sync: ", err.Error())
				}
			}
		}
	}()

	return bridge, nil
}

func (bridge Bridge) getMqttReg(name string) (*MqttReg, bool) {
	bridge.mqttRegsMutex.Lock()
	defer bridge.mqttRegsMutex.Unlock()

	reg, ok := bridge.mqttRegs[name]
	return reg, ok
}

func (bridge Bridge) isMqttReg(name string) bool {
	_, ok := bridge.getMqttReg(name)
	return ok
}

func (bridge Bridge) getSurpReg(name string) (*SurpReg, bool) {
	bridge.surpMutex.Lock()
	defer bridge.surpMutex.Unlock()

	reg, ok := bridge.surpRegs[name]
	return reg, ok
}

func (bridge Bridge) isSurpReg(name string) bool {
	_, ok := bridge.getSurpReg(name)
	return ok
}

func (bridge Bridge) handleMqttMetadata(name string, metadata goreg.Metadata) error {
	if bridge.isMqttReg(name) || bridge.isSurpReg(name) {
		return nil
	}

	bridge.mqttRegsMutex.Lock()
	defer bridge.mqttRegsMutex.Unlock()

	bridge.mqttRegs[name] = &MqttReg{
		name:      name,
		metadata:  metadata,
		registers: bridge.registers,
	}

	return nil
}

func (bridge Bridge) handleMqttSync(name string, jsonValue []byte) error {

	reg, ok := bridge.getMqttReg(name)
	if !ok {
		return nil
	}

	var value any
	if len(jsonValue) != 0 {
		err := json.Unmarshal(jsonValue, &value)
		if err != nil {
			println("Error deserializing value: ", err.Error())
			return nil
		}
	}

	if reg.surpType == "" {
		st := fmt.Sprintf("%T", value)
		end := len(st)
		for i := len(st) - 1; i >= 0; i-- {
			if st[i] < '0' || st[i] > '9' {
				end = i
				break
			}
		}
		st = st[:end+1]

		if st == "string" || st == "int" || st == "bool" || st == "float" {
			reg.surpType = st
			fmt.Printf("New MQTT register %s of SURP type %s\n", name, reg.surpType)
			reg.metadata["type"] = reg.surpType
			reg.metadata["rw"] = "true"
			bridge.surpGroup.AddProviders(reg)
		}
	}

	if reg.surpSyncListener != nil && reg.value != value {
		reg.value = value
		reg.surpSyncListener()
	}

	return nil
}

func (reg *MqttReg) GetName() string {
	return reg.name
}

func (reg *MqttReg) GetEncodedValue() (surp.Optional[[]byte], map[string]string) {
	var encoded surp.Optional[[]byte]
	v := reg.value
	if v != nil {
		encoded = surp.NewDefined(surp.EncodeGeneric(v, reg.surpType))
	}
	return encoded, reg.metadata
}

func (reg *MqttReg) SetEncodedValue(encoded surp.Optional[[]byte]) {

	var decoded any
	if encoded.IsDefined() {
		decoded, _ = surp.DecodeGeneric(encoded.Get(), reg.surpType)
	}

	jsonValue, err := json.Marshal(decoded)
	if err != nil {
		println("Error serializing value to json: ", err.Error())
		return
	}

	reg.registers.SetValue(reg.name, jsonValue)
}

func (reg *MqttReg) Attach(syncListener func()) {
	reg.surpSyncListener = syncListener
}

func jsonSerializer(value any) []byte {
	jsonValue, err := json.Marshal(value)
	if err != nil {
		println("Error serializing value to json: ", err.Error())
		return nil
	}
	return jsonValue
}

func jsonDeserializer(jsonValue []byte) any {
	var value any
	err := json.Unmarshal(jsonValue, &value)
	if err != nil {
		println("Error deserializing value from json: ", err.Error())
		return nil
	}
	return value
}

func (bridge Bridge) handleSurpSync(msg *surp.Message) {

	name := msg.Name

	if bridge.isMqttReg(name) {
		return
	}

	reg, ok := bridge.getSurpReg(name)
	if !ok {

		fmt.Printf("New SURP register %s of type %s\n", name, msg.Metadata["type"])

		reader, writer := goreg.Provide(bridge.registers, name, jsonSerializer, jsonDeserializer, msg.Metadata)

		reg = &SurpReg{
			name:     name,
			writer:   writer,
			surpType: msg.Metadata["type"],
		}
		bridge.surpGroup.AddConsumers(reg)

		bridge.surpMutex.Lock()
		bridge.surpRegs[name] = reg
		bridge.surpMutex.Unlock()

		go func() {
			for {
				select {
				case value := <-reader:
					if reg.setListener != nil && reg.value != value {
						reg.value = value
						var encoded surp.Optional[[]byte]
						if value != nil {

							// if value is float and surpType is int, convert to int
							if reg.surpType == "int" {
								if f, ok := value.(float64); ok {
									value = int64(f)
								}
							}

							encoded = surp.NewDefined(surp.EncodeGeneric(value, reg.surpType))
						}
						reg.setListener(encoded)
					}
				case <-bridge.context.Done():
					return
				}
			}
		}()
	}

}

func (reg *SurpReg) GetName() string {
	return reg.name
}

func (reg *SurpReg) SetMetadata(map[string]string) {
}

func (reg *SurpReg) SyncValue(encoded surp.Optional[[]byte]) {
	var decoded any
	if encoded.IsDefined() {
		d, ok := surp.DecodeGeneric(encoded.Get(), reg.surpType)
		if !ok {
			println("Error decoding SURP value")
			return
		}
		decoded = d
	}
	if reg.value != decoded {
		reg.value = decoded
		reg.writer <- decoded
	}
}

func (reg *SurpReg) Attach(setListener func(surp.Optional[[]byte])) {
	reg.setListener = setListener
}
