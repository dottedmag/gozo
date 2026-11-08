package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metrics"
)

func scrapeMetrics() string {
	var b bytes.Buffer
	metrics.WritePrometheus(&b, false)
	return b.String()
}

func TestDeviceReadingsAndLifecycle(t *testing.T) {
	m := &monitor{
		devices: make(map[string]deviceInfo),
		online:  make(map[string]bool),
		values:  make(map[string]map[string]string),
		pending: make(map[string]pendingState),
	}
	t.Cleanup(func() { m.handleDeviceList(json.RawMessage(`[]`)) })

	// The frontend can replay a state before the device inventory arrives.
	m.handleDeviceState("study-sensor", json.RawMessage(`{"temperature":21.5,"humidity":48,"battery":86,"linkquality":97,"voltage":3010,"temperature_2":77}`))
	m.handleDeviceState("study-sensor", json.RawMessage(`{"temperature":22.5}`))
	m.handleAvailability("study-sensor", json.RawMessage(`{"state":"online"}`))
	m.handleDeviceList(json.RawMessage(`[
		{"friendly_name":"coordinator","ieee_address":"0x0000","type":"Coordinator"},
		{"friendly_name":"study-sensor","ieee_address":"0x1111","type":"EndDevice","definition":{"exposes":[
			{"type":"numeric","name":"temperature","property":"temperature","unit":"°C"},
			{"type":"numeric","name":"temperature","property":"temperature_2","unit":"°F"},
			{"type":"numeric","name":"humidity","property":"humidity","unit":"%"},
			{"type":"numeric","name":"battery","property":"battery","unit":"%"},
			{"type":"numeric","name":"voltage","property":"voltage","unit":"mV"},
			{"type":"numeric","name":"linkquality","property":"linkquality"}
		]}},
		{"friendly_name":"hall-dimmer","ieee_address":"0x2222","type":"Router","definition":{"exposes":[
			{"type":"light","features":[
				{"type":"binary","name":"state","property":"state","value_on":"ON","value_off":"OFF"},
				{"type":"numeric","name":"brightness","property":"brightness","value_max":254}
			]},
			{"type":"numeric","name":"power","property":"power","unit":"W"},
			{"type":"numeric","name":"energy","property":"energy","unit":"Wh"},
			{"type":"numeric","name":"voltage","property":"voltage","unit":"V"}
		]}}
	]`))
	m.handleDeviceState("hall-dimmer", json.RawMessage(`{"state":"ON","brightness":127,"power":21.5,"energy":1500,"voltage":230}`))

	scrape := scrapeMetrics()
	for _, want := range []string{
		`zigbee_device_up{name="study-sensor", ieee="0x1111"} 1`,
		`zigbee_temperature_celsius{name="study-sensor", ieee="0x1111", property="temperature"} 22.5`,
		`zigbee_temperature_celsius{name="study-sensor", ieee="0x1111", property="temperature_2"} 25`,
		`zigbee_humidity_percent{name="study-sensor", ieee="0x1111", property="humidity"} 48`,
		`zigbee_battery_percent{name="study-sensor", ieee="0x1111", property="battery"} 86`,
		`zigbee_linkquality{name="study-sensor", ieee="0x1111", property="linkquality"} 97`,
		`zigbee_voltage_volts{name="study-sensor", ieee="0x1111", property="voltage"} 3.01`,
		`zigbee_light_on{name="hall-dimmer", ieee="0x2222", property="state"} 1`,
		`zigbee_brightness_level{name="hall-dimmer", ieee="0x2222", property="brightness"} 127`,
		`zigbee_power_watts{name="hall-dimmer", ieee="0x2222", property="power"} 21.5`,
		`zigbee_energy_kwh{name="hall-dimmer", ieee="0x2222", property="energy"} 1.5`,
		`zigbee_voltage_volts{name="hall-dimmer", ieee="0x2222", property="voltage"} 230`,
		`zigbee_device_last_message_timestamp_seconds{name="study-sensor", ieee="0x1111"} `,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("missing %q in:\n%s", want, scrape)
		}
	}
	if strings.Contains(scrape, `zigbee_device_up{name="coordinator"`) {
		t.Error("coordinator was exported as a device")
	}

	m.handleDeviceState("hall-dimmer", json.RawMessage(`{"state":"OFF"}`))
	scrape = scrapeMetrics()
	if !strings.Contains(scrape, `zigbee_light_on{name="hall-dimmer", ieee="0x2222", property="state"} 0`) ||
		!strings.Contains(scrape, `zigbee_brightness_level{name="hall-dimmer", ieee="0x2222", property="brightness"} 127`) {
		t.Error("partial state update lost an existing reading")
	}
	m.handleDeviceState("hall-dimmer", json.RawMessage(`{"brightness":null}`))
	if strings.Contains(scrapeMetrics(), `zigbee_brightness_level{name="hall-dimmer"`) {
		t.Error("null brightness reading was not removed")
	}

	m.handleDeviceList(json.RawMessage(`[{"friendly_name":"study-sensor","ieee_address":"0x1111","type":"EndDevice"}]`))
	if strings.Contains(scrapeMetrics(), `ieee="0x2222"`) {
		t.Error("removed dimmer metrics remain exported")
	}
	m.resetForConnection()
	scrape = scrapeMetrics()
	if strings.Contains(scrape, `zigbee_temperature_celsius{name="study-sensor"`) ||
		strings.Contains(scrape, `zigbee_device_last_message_timestamp_seconds{name="study-sensor"`) ||
		!strings.Contains(scrape, `zigbee_device_up{name="study-sensor", ieee="0x1111"} 0`) {
		t.Error("reconnection did not clear cached readings and availability")
	}
}

func TestConnectionMetric(t *testing.T) {
	t.Cleanup(func() { metrics.UnregisterMetric("zigbee_monitor_connected") })
	setConnectionMetric(false)
	if !strings.Contains(scrapeMetrics(), "zigbee_monitor_connected 0") {
		t.Error("disconnected state was not exported")
	}
	setConnectionMetric(true)
	if !strings.Contains(scrapeMetrics(), "zigbee_monitor_connected 1") {
		t.Error("connected state was not exported")
	}
}

func TestDeviceStateIgnoresAttributeTopics(t *testing.T) {
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })

	m := &monitor{}
	t.Cleanup(func() { m.handleDeviceList(json.RawMessage(`[]`)) })

	// Friendly names may contain slashes, so distinguish device and attribute
	// topics using the inventory rather than rejecting every topic with a slash.
	const name = "room/hall-dimmer"
	attributes := []struct {
		topic   string
		payload string
	}{
		{"state", `"OFF"`},
		{"brightness", `254`},
		{"enabled", `false`},
		{"type", `{"brightness":254}`},
		{"unknown", `null`},
		{"colors", `[1,2,3]`},
	}
	sendAttributes := func() {
		for _, attr := range attributes {
			m.handleDeviceState(name+"/"+attr.topic, json.RawMessage(attr.payload))
		}
	}

	// Replayed attributes can precede bridge/devices, alongside real state.
	m.handleDeviceState(name, json.RawMessage(`{"state":"ON","brightness":127}`))
	sendAttributes()
	m.handleDeviceList(json.RawMessage(`[
		{"friendly_name":"room/hall-dimmer","ieee_address":"0x3333","type":"Router"}
	]`))
	sendAttributes()

	if strings.Contains(logs.String(), "failed to parse state") {
		t.Errorf("attribute messages produced parse errors:\n%s", &logs)
	}
	scrape := scrapeMetrics()
	for _, want := range []string{
		`zigbee_light_on{name="room/hall-dimmer", ieee="0x3333", property="state"} 1`,
		`zigbee_brightness_level{name="room/hall-dimmer", ieee="0x3333", property="brightness"} 127`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("missing %q after attribute messages:\n%s", want, scrape)
		}
	}
	if len(m.values) != 1 || len(m.pending) != 0 {
		t.Errorf("attribute topics retained as devices: values=%v pending=%v", m.values, m.pending)
	}

	// A malformed payload on a known device topic must still be reported.
	logs.Reset()
	m.handleDeviceState(name, json.RawMessage(`"OFF"`))
	if !strings.Contains(logs.String(), "failed to parse state for "+name+":") {
		t.Error("invalid state for a known device was silently ignored")
	}
}
