package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/gorilla/websocket"
)

type nodeInfo struct {
	NodeID           int         `json:"nodeId"`
	Name             string      `json:"name"`
	Location         string      `json:"location"`
	Status           int         `json:"status"`
	IsControllerNode bool        `json:"isControllerNode"`
	Values           []valueInfo `json:"values"`
	reported         map[valueKey]reportedValue
}

type valueInfo struct {
	CommandClass int `json:"commandClass"`
	Endpoint     int `json:"endpoint"`
	Property     any `json:"property"`
	PropertyKey  any `json:"propertyKey"`
	Value        any `json:"value"`
	NewValue     any `json:"newValue"`
	Metadata     struct {
		Label string `json:"label"`
		Unit  string `json:"unit"`
	} `json:"metadata"`
}

type valueKey struct {
	commandClass int
	endpoint     int
	property     string
	propertyKey  string
}

type reportedValue struct {
	info       valueInfo
	metricName string
}

type zwaveEvent struct {
	Source    string          `json:"source"`
	Event     string          `json:"event"`
	NodeID    int             `json:"nodeId"`
	Args      json.RawMessage `json:"args"`
	Node      *nodeInfo       `json:"node"`
	NodeState *nodeInfo       `json:"nodeState"`
}

func (v valueInfo) key() valueKey {
	property, _ := json.Marshal(v.Property)
	propertyKey, _ := json.Marshal(v.PropertyKey)
	return valueKey{v.CommandClass, v.Endpoint, string(property), string(propertyKey)}
}

func (n nodeInfo) displayName() string {
	var parts []string
	if n.Location != "" {
		parts = append(parts, n.Location)
	}
	if n.Name != "" {
		parts = append(parts, n.Name)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return fmt.Sprintf("Node %d", n.NodeID)
}

func nodeMetricName(nodeID int, name string) string {
	return fmt.Sprintf(`zwave_device_up{node_id="%d", name=%q}`, nodeID, name)
}

func nodeLastValueUpdateMetricName(n nodeInfo) string {
	return fmt.Sprintf(`zwave_node_last_value_update_timestamp_seconds{node_id="%d", name=%q}`, n.NodeID, n.displayName())
}

func setConnectionMetric(connected bool) {
	var value float64
	if connected {
		value = 1
	}
	metrics.GetOrCreateGauge("zwave_monitor_connected", nil).Set(value)
}

func celsius(value float64, unit string) (float64, bool) {
	switch unit {
	case "°C", "C":
		return value, true
	case "°F", "F":
		return (value - 32) * 5 / 9, true
	default:
		return 0, false
	}
}

func measurementMetricName(node nodeInfo, v valueInfo) (string, float64, bool) {
	property, _ := v.Property.(string)
	sensor := property
	if sensor == "" {
		sensor = v.Metadata.Label
	}
	label := strings.ToLower(strings.ReplaceAll(sensor, "₂", "2"))
	base := fmt.Sprintf(`node_id="%d", name=%q, endpoint="%d"`, node.NodeID, node.displayName(), v.Endpoint)
	if v.CommandClass == 37 && property == "currentValue" { // Binary Switch
		if on, ok := v.Value.(bool); ok {
			if on {
				return fmt.Sprintf(`zwave_switch_on{%s}`, base), 1, true
			}
			return fmt.Sprintf(`zwave_switch_on{%s}`, base), 0, true
		}
	}
	value, ok := v.Value.(float64)
	if !ok {
		return "", 0, false
	}

	switch v.CommandClass {
	case 64: // Thermostat Mode
		if property == "mode" {
			return fmt.Sprintf(`zwave_thermostat_mode{%s}`, base), value, true
		}
	case 66: // Thermostat Operating State
		if property == "state" && (value == 0 || value == 1) {
			return fmt.Sprintf(`zwave_thermostat_heating{%s}`, base), value, true
		}
	case 67: // Thermostat Setpoint
		if property == "setpoint" {
			if degrees, ok := celsius(value, v.Metadata.Unit); ok {
				return fmt.Sprintf(`zwave_temperature_setpoint_celsius{%s, setpoint=%q}`, base, fmt.Sprint(v.PropertyKey)), degrees, true
			}
		}
	case 50: // Meter
		if property == "value" {
			meter := fmt.Sprintf(`%s, property_key=%q`, base, fmt.Sprint(v.PropertyKey))
			switch v.Metadata.Unit {
			case "W":
				return fmt.Sprintf(`zwave_power_watts{%s}`, meter), value, true
			case "kWh":
				return fmt.Sprintf(`zwave_energy_kwh{%s}`, meter), value, true
			case "Wh":
				return fmt.Sprintf(`zwave_energy_kwh{%s}`, meter), value / 1000, true
			case "V":
				return fmt.Sprintf(`zwave_voltage_volts{%s}`, meter), value, true
			case "":
				// The common electric meter property keys identify the scale.
				switch v.PropertyKey {
				case float64(66049):
					return fmt.Sprintf(`zwave_power_watts{%s}`, meter), value, true
				case float64(65537):
					return fmt.Sprintf(`zwave_energy_kwh{%s}`, meter), value, true
				case float64(66561):
					return fmt.Sprintf(`zwave_voltage_volts{%s}`, meter), value, true
				}
			}
		}
	case 49: // Multilevel Sensor
		switch {
		case strings.Contains(label, "temperature"):
			if degrees, ok := celsius(value, v.Metadata.Unit); ok {
				return fmt.Sprintf(`zwave_temperature_celsius{%s, sensor=%q}`, base, sensor), degrees, true
			}
		case strings.Contains(label, "humidity") && strings.HasPrefix(v.Metadata.Unit, "%"):
			return fmt.Sprintf(`zwave_humidity_percent{%s}`, base), value, true
		case (strings.Contains(label, "carbon dioxide") || strings.Contains(label, "co2")) && v.Metadata.Unit == "ppm":
			return fmt.Sprintf(`zwave_co2_ppm{%s}`, base), value, true
		}
	}
	return "", 0, false
}

func statusName(status int) string {
	switch status {
	case 0:
		return "unknown"
	case 1:
		return "asleep"
	case 2:
		return "awake"
	case 3:
		return "dead"
	case 4:
		return "alive"
	default:
		return fmt.Sprintf("status(%d)", status)
	}
}

func isUp(status int) bool {
	return status == 1 || status == 2 || status == 4 // asleep, awake, alive
}

type monitor struct {
	mu    sync.Mutex
	nodes map[int]*nodeInfo
}

func (m *monitor) removeNodeLocked(nodeID int) {
	n := m.nodes[nodeID]
	if n == nil {
		return
	}
	metrics.UnregisterMetric(nodeMetricName(n.NodeID, n.displayName()))
	metrics.UnregisterMetric(nodeLastValueUpdateMetricName(*n))
	for _, reported := range n.reported {
		if reported.metricName != "" {
			metrics.UnregisterMetric(reported.metricName)
		}
	}
	delete(m.nodes, nodeID)
}

func (m *monitor) setValueLocked(n *nodeInfo, v valueInfo, hasValue bool) {
	if v.CommandClass != 37 && v.CommandClass != 49 && v.CommandClass != 50 && v.CommandClass != 64 && v.CommandClass != 66 && v.CommandClass != 67 {
		return
	}
	key := v.key()
	reported := n.reported[key]
	if v.Metadata.Unit == "" {
		v.Metadata.Unit = reported.info.Metadata.Unit
	}
	if v.Metadata.Label == "" {
		v.Metadata.Label = reported.info.Metadata.Label
	}
	if !hasValue {
		v.Value = reported.info.Value
	}
	metricName, value, valid := measurementMetricName(*n, v)
	if reported.metricName != "" && reported.metricName != metricName {
		metrics.UnregisterMetric(reported.metricName)
	}
	if valid {
		metrics.GetOrCreateGauge(metricName, nil).Set(value)
	} else {
		metricName = ""
	}
	n.reported[key] = reportedValue{info: v, metricName: metricName}
}

func (m *monitor) setNodeLocked(n nodeInfo) {
	m.removeNodeLocked(n.NodeID)
	if n.IsControllerNode {
		return
	}
	n.reported = make(map[valueKey]reportedValue)
	m.nodes[n.NodeID] = &n
	var up float64
	if isUp(n.Status) {
		up = 1
	}
	metrics.GetOrCreateGauge(nodeMetricName(n.NodeID, n.displayName()), nil).Set(up)
	for _, v := range n.Values {
		m.setValueLocked(&n, v, true)
	}
	log.Printf("node %s (id=%d): %s", n.displayName(), n.NodeID, statusName(n.Status))
}

func (m *monitor) updateFromNodes(nodes []nodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.nodes {
		m.removeNodeLocked(id)
	}
	for _, n := range nodes {
		m.setNodeLocked(n)
	}
}

func (m *monitor) handleEvent(event zwaveEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if event.Source == "controller" {
		switch event.Event {
		case "node added":
			if event.Node != nil {
				m.setNodeLocked(*event.Node)
			}
		case "node removed":
			if event.Node != nil {
				m.removeNodeLocked(event.Node.NodeID)
			}
		}
		return
	}
	if event.Source != "node" {
		return
	}
	if event.Event == "ready" && event.NodeState != nil {
		m.setNodeLocked(*event.NodeState)
		return
	}

	node := m.nodes[event.NodeID]
	if node == nil {
		return
	}

	switch event.Event {
	case "value added", "value updated", "value removed", "metadata updated":
		var v valueInfo
		if err := json.Unmarshal(event.Args, &v); err != nil {
			log.Printf("invalid %s event for node %d: %v", event.Event, event.NodeID, err)
			return
		}
		switch event.Event {
		case "value removed":
			key := v.key()
			if old := node.reported[key]; old.metricName != "" {
				metrics.UnregisterMetric(old.metricName)
			}
			delete(node.reported, key)
		case "metadata updated":
			m.setValueLocked(node, v, false)
		default:
			v.Value = v.NewValue
			m.setValueLocked(node, v, true)
			if v.NewValue != nil {
				metrics.GetOrCreateGauge(nodeLastValueUpdateMetricName(*node), nil).Set(float64(time.Now().Unix()))
			}
		}
		return
	}

	var newStatus int
	switch event.Event {
	case "alive":
		newStatus = 4
	case "dead":
		newStatus = 3
	case "sleep":
		newStatus = 1
	case "wake up":
		newStatus = 2
	default:
		return
	}

	oldStatus := node.Status
	if oldStatus == newStatus {
		return
	}

	node.Status = newStatus
	name := node.displayName()

	var value float64
	if isUp(newStatus) {
		value = 1
	}
	metrics.GetOrCreateGauge(nodeMetricName(event.NodeID, name), nil).Set(value)

	log.Printf("node %s (id=%d): %s -> %s", name, event.NodeID, statusName(oldStatus), statusName(newStatus))
}

func connect(wsURL string) ([]nodeInfo, *websocket.Conn, error) {
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	c.SetReadLimit(10 * 1024 * 1024)

	// Handshake
	_, data, err := c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read handshake: %w", err)
	}
	var handshake struct {
		MaxSchemaVersion int `json:"maxSchemaVersion"`
	}
	if err := json.Unmarshal(data, &handshake); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse handshake: %w", err)
	}

	// set_api_schema
	if err := c.WriteJSON(map[string]any{
		"command":       "set_api_schema",
		"messageId":     1,
		"schemaVersion": handshake.MaxSchemaVersion,
	}); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("write set_api_schema: %w", err)
	}
	_, data, err = c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read set_api_schema response: %w", err)
	}
	var schemaResp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(data, &schemaResp); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse set_api_schema response: %w", err)
	}
	if !schemaResp.Success {
		c.Close()
		return nil, nil, fmt.Errorf("set_api_schema failed")
	}

	// start_listening
	if err := c.WriteJSON(map[string]any{
		"command":   "start_listening",
		"messageId": 2,
	}); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("write start_listening: %w", err)
	}
	_, data, err = c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read start_listening response: %w", err)
	}

	var resp struct {
		Success bool `json:"success"`
		Result  struct {
			State struct {
				Nodes []nodeInfo `json:"nodes"`
			} `json:"state"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse start_listening: %w", err)
	}
	if !resp.Success {
		c.Close()
		return nil, nil, fmt.Errorf("start_listening failed")
	}

	return resp.Result.State.Nodes, c, nil
}

func runMonitor(wsURL string, m *monitor) {
	setConnectionMetric(false)
	for {
		nodes, conn, err := connect(wsURL)
		if err != nil {
			log.Printf("connection error: %v, retrying in 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}

		m.updateFromNodes(nodes)
		setConnectionMetric(true)
		log.Printf("connected, monitoring %d nodes", len(m.nodes))

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("read error: %v, reconnecting", err)
				setConnectionMetric(false)
				conn.Close()
				break
			}

			var msg struct {
				Type  string      `json:"type"`
				Event *zwaveEvent `json:"event"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("parse error: %v", err)
				continue
			}

			if msg.Type == "event" && msg.Event != nil {
				m.handleEvent(*msg.Event)
			}
		}

		time.Sleep(5 * time.Second)
	}
}

func main() {
	zwaveAddr := flag.String("zwavejs", "ws://localhost:3000", "Z-Wave JS WebSocket API address")
	metricsPort := flag.Int("port", 9098, "Prometheus metrics port")
	flag.Parse()

	m := &monitor{
		nodes: make(map[int]*nodeInfo),
	}
	setConnectionMetric(false)

	go func() {
		http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.WritePrometheus(w, false)
		})
		log.Printf("serving metrics on :%d", *metricsPort)
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *metricsPort), nil))
	}()

	runMonitor(*zwaveAddr, m)
}
