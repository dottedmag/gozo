package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metrics"
)

func scrapeMetrics() string {
	var b bytes.Buffer
	metrics.WritePrometheus(&b, false)
	return b.String()
}

func TestMeasurementsFromStateAndEvents(t *testing.T) {
	m := &monitor{nodes: make(map[int]*nodeInfo)}
	t.Cleanup(func() { m.updateFromNodes(nil) })

	var nodes []nodeInfo
	state := `[
		{"nodeId":11,"name":"ZW096","status":4,"values":[
			{"commandClass":37,"endpoint":0,"property":"currentValue","value":true},
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":66049,"value":42.5,"metadata":{"unit":"W"}},
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":65537,"value":12.3,"metadata":{"unit":"kWh"}},
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":66561,"value":231,"metadata":{"unit":"V"}}
		]},
		{"nodeId":12,"name":"ZW132","status":4,"values":[
			{"commandClass":37,"endpoint":1,"property":"currentValue","value":false},
			{"commandClass":37,"endpoint":2,"property":"currentValue","value":true},
			{"commandClass":50,"endpoint":1,"property":"value","propertyKey":66049,"value":7,"metadata":{"unit":"W"}}
		]},
		{"nodeId":13,"name":"MH9-CO2","status":4,"values":[
			{"commandClass":49,"endpoint":0,"property":"Air temperature","value":21.5,"metadata":{"unit":"°C"}},
			{"commandClass":49,"endpoint":0,"property":"Humidity","value":48,"metadata":{"unit":"%"}},
			{"commandClass":49,"endpoint":0,"property":"Carbon dioxide (CO₂) level","value":860,"metadata":{"unit":"ppm"}}
		]},
		{"nodeId":14,"name":"Z-TRM3","status":4,"values":[
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":66049,"value":900,"metadata":{"unit":"W"}},
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":65537,"value":18.5,"metadata":{"unit":"kWh"}},
			{"commandClass":50,"endpoint":0,"property":"value","propertyKey":66561,"value":229,"metadata":{"unit":"V"}},
			{"commandClass":64,"endpoint":0,"property":"mode","value":1},
			{"commandClass":66,"endpoint":0,"property":"state","value":1},
			{"commandClass":67,"endpoint":0,"property":"setpoint","propertyKey":1,"value":23,"metadata":{"unit":"°C"}},
			{"commandClass":49,"endpoint":2,"property":"Air temperature","value":22,"metadata":{"unit":"°C"}},
			{"commandClass":49,"endpoint":3,"property":"Air temperature","value":0,"metadata":{"unit":"°C"}},
			{"commandClass":49,"endpoint":4,"property":"Air temperature","value":28,"metadata":{"unit":"°C"}}
		]}
	]`
	if err := json.Unmarshal([]byte(state), &nodes); err != nil {
		t.Fatal(err)
	}
	m.updateFromNodes(nodes)

	scrape := scrapeMetrics()
	for _, want := range []string{
		`zwave_switch_on{node_id="11", name="ZW096", endpoint="0"} 1`,
		`zwave_switch_on{node_id="12", name="ZW132", endpoint="1"} 0`,
		`zwave_switch_on{node_id="12", name="ZW132", endpoint="2"} 1`,
		`zwave_power_watts{node_id="11", name="ZW096", endpoint="0", property_key="66049"} 42.5`,
		`zwave_energy_kwh{node_id="11", name="ZW096", endpoint="0", property_key="65537"} 12.3`,
		`zwave_voltage_volts{node_id="11", name="ZW096", endpoint="0", property_key="66561"} 231`,
		`zwave_power_watts{node_id="12", name="ZW132", endpoint="1", property_key="66049"} 7`,
		`zwave_temperature_celsius{node_id="13", name="MH9-CO2", endpoint="0", sensor="Air temperature"} 21.5`,
		`zwave_humidity_percent{node_id="13", name="MH9-CO2", endpoint="0"} 48`,
		`zwave_co2_ppm{node_id="13", name="MH9-CO2", endpoint="0"} 860`,
		`zwave_temperature_celsius{node_id="14", name="Z-TRM3", endpoint="3", sensor="Air temperature"} 0`,
		`zwave_temperature_setpoint_celsius{node_id="14", name="Z-TRM3", endpoint="0", setpoint="1"} 23`,
		`zwave_power_watts{node_id="14", name="Z-TRM3", endpoint="0", property_key="66049"} 900`,
		`zwave_energy_kwh{node_id="14", name="Z-TRM3", endpoint="0", property_key="65537"} 18.5`,
		`zwave_voltage_volts{node_id="14", name="Z-TRM3", endpoint="0", property_key="66561"} 229`,
		`zwave_thermostat_mode{node_id="14", name="Z-TRM3", endpoint="0"} 1`,
		`zwave_thermostat_heating{node_id="14", name="Z-TRM3", endpoint="0"} 1`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("missing metric %q in:\n%s", want, scrape)
		}
	}
	if strings.Contains(scrape, `zwave_node_last_value_update_timestamp_seconds{`) {
		t.Error("initial cached values were given a fresh report timestamp")
	}

	m.handleEvent(zwaveEvent{Source: "node", Event: "value updated", NodeID: 13,
		Args: json.RawMessage(`{"commandClass":49,"endpoint":0,"property":"Carbon dioxide (CO₂) level","newValue":920}`)})
	if !strings.Contains(scrapeMetrics(), `zwave_co2_ppm{node_id="13", name="MH9-CO2", endpoint="0"} 920`) {
		t.Error("CO2 update was not exported")
	}
	if !strings.Contains(scrapeMetrics(), `zwave_node_last_value_update_timestamp_seconds{node_id="13", name="MH9-CO2"} `) {
		t.Error("last value update was not exported")
	}
	m.handleEvent(zwaveEvent{Source: "node", Event: "value updated", NodeID: 14,
		Args: json.RawMessage(`{"commandClass":66,"endpoint":0,"property":"state","newValue":0}`)})
	if !strings.Contains(scrapeMetrics(), `zwave_thermostat_heating{node_id="14", name="Z-TRM3", endpoint="0"} 0`) {
		t.Error("thermostat heating state was not updated")
	}
	m.handleEvent(zwaveEvent{Source: "node", Event: "value updated", NodeID: 12,
		Args: json.RawMessage(`{"commandClass":37,"endpoint":1,"property":"currentValue","newValue":true}`)})
	if !strings.Contains(scrapeMetrics(), `zwave_switch_on{node_id="12", name="ZW132", endpoint="1"} 1`) {
		t.Error("switch state was not updated")
	}

	m.handleEvent(zwaveEvent{Source: "node", Event: "value removed", NodeID: 13,
		Args: json.RawMessage(`{"commandClass":49,"endpoint":0,"property":"Humidity"}`)})
	if strings.Contains(scrapeMetrics(), `zwave_humidity_percent{node_id="13"`) {
		t.Error("removed humidity value remains exported")
	}

	m.handleEvent(zwaveEvent{Source: "controller", Event: "node removed", Node: &nodeInfo{NodeID: 14}})
	if strings.Contains(scrapeMetrics(), `node_id="14"`) {
		t.Error("removed thermostat metrics remain exported")
	}
}

func TestFahrenheitAndAddedValue(t *testing.T) {
	m := &monitor{nodes: make(map[int]*nodeInfo)}
	t.Cleanup(func() { m.updateFromNodes(nil) })
	m.updateFromNodes([]nodeInfo{{NodeID: 21, Name: "Thermostat", Status: 4}})
	m.handleEvent(zwaveEvent{Source: "node", Event: "value added", NodeID: 21,
		Args: json.RawMessage(`{"commandClass":49,"endpoint":2,"property":"Air temperature","newValue":77}`)})
	if strings.Contains(scrapeMetrics(), `zwave_temperature_celsius{node_id="21"`) {
		t.Error("temperature without a unit was exported")
	}
	m.handleEvent(zwaveEvent{Source: "node", Event: "metadata updated", NodeID: 21,
		Args: json.RawMessage(`{"commandClass":49,"endpoint":2,"property":"Air temperature","metadata":{"unit":"°F"}}`)})
	if !strings.Contains(scrapeMetrics(), `zwave_temperature_celsius{node_id="21", name="Thermostat", endpoint="2", sensor="Air temperature"} 25`) {
		t.Error("Fahrenheit temperature was not converted")
	}
	m.handleEvent(zwaveEvent{Source: "node", Event: "value added", NodeID: 21,
		Args: json.RawMessage(`{"commandClass":50,"endpoint":0,"property":"value","propertyKey":65537,"newValue":1250,"metadata":{"unit":"Wh"}}`)})
	if !strings.Contains(scrapeMetrics(), `zwave_energy_kwh{node_id="21", name="Thermostat", endpoint="0", property_key="65537"} 1.25`) {
		t.Error("watt-hours were not converted to kilowatt-hours")
	}
}

func TestConnectionMetric(t *testing.T) {
	t.Cleanup(func() { metrics.UnregisterMetric("zwave_monitor_connected") })
	setConnectionMetric(false)
	if !strings.Contains(scrapeMetrics(), "zwave_monitor_connected 0") {
		t.Error("disconnected state was not exported")
	}
	setConnectionMetric(true)
	if !strings.Contains(scrapeMetrics(), "zwave_monitor_connected 1") {
		t.Error("connected state was not exported")
	}
}
