package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/gorilla/websocket"
)

type deviceInfo struct {
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	IEEEAddress  string `json:"ieee_address"`
	Definition   struct {
		Exposes []expose `json:"exposes"`
	} `json:"definition"`
}

type expose struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Property string   `json:"property"`
	Unit     string   `json:"unit"`
	ValueOn  any      `json:"value_on"`
	ValueOff any      `json:"value_off"`
	Features []expose `json:"features"`
}

func deviceMetricName(name, ieee string) string {
	return fmt.Sprintf(`zigbee_device_up{name=%q, ieee=%q}`, name, ieee)
}

func lastMessageMetricName(name, ieee string) string {
	return fmt.Sprintf(`zigbee_device_last_message_timestamp_seconds{name=%q, ieee=%q}`, name, ieee)
}

func setConnectionMetric(connected bool) {
	var value float64
	if connected {
		value = 1
	}
	metrics.GetOrCreateGauge("zigbee_monitor_connected", nil).Set(value)
}

func findExpose(exposes []expose, property string) (expose, bool) {
	for _, e := range exposes {
		if e.Property == property {
			return e, true
		}
		if nested, ok := findExpose(e.Features, property); ok {
			return nested, true
		}
	}
	return expose{}, false
}

func metricForValue(info deviceInfo, property string, raw any) (string, float64, bool) {
	e, found := findExpose(info.Definition.Exposes, property)
	name := property
	if found && e.Name != "" {
		name = e.Name
	}
	labels := fmt.Sprintf(`name=%q, ieee=%q, property=%q`, info.FriendlyName, info.IEEEAddress, property)
	if name == "state" {
		var on, known bool
		switch v := raw.(type) {
		case string:
			on, known = strings.EqualFold(v, "ON"), strings.EqualFold(v, "ON") || strings.EqualFold(v, "OFF")
		case bool:
			on, known = v, true
		}
		if found && e.ValueOn != nil && reflect.DeepEqual(raw, e.ValueOn) {
			on, known = true, true
		} else if found && e.ValueOff != nil && reflect.DeepEqual(raw, e.ValueOff) {
			on, known = false, true
		}
		if known {
			if on {
				return fmt.Sprintf(`zigbee_light_on{%s}`, labels), 1, true
			}
			return fmt.Sprintf(`zigbee_light_on{%s}`, labels), 0, true
		}
		return "", 0, false
	}
	value, ok := raw.(float64)
	if !ok {
		return "", 0, false
	}
	switch name {
	case "temperature":
		switch e.Unit {
		case "", "°C", "C":
			return fmt.Sprintf(`zigbee_temperature_celsius{%s}`, labels), value, true
		case "°F", "F":
			return fmt.Sprintf(`zigbee_temperature_celsius{%s}`, labels), (value - 32) * 5 / 9, true
		}
	case "humidity":
		if e.Unit == "" || e.Unit == "%" {
			return fmt.Sprintf(`zigbee_humidity_percent{%s}`, labels), value, true
		}
	case "brightness":
		return fmt.Sprintf(`zigbee_brightness_level{%s}`, labels), value, true
	case "power":
		if e.Unit == "" || e.Unit == "W" {
			return fmt.Sprintf(`zigbee_power_watts{%s}`, labels), value, true
		}
	case "energy":
		switch e.Unit {
		case "", "kWh":
			return fmt.Sprintf(`zigbee_energy_kwh{%s}`, labels), value, true
		case "Wh":
			return fmt.Sprintf(`zigbee_energy_kwh{%s}`, labels), value / 1000, true
		}
	case "voltage":
		switch e.Unit {
		case "V":
			return fmt.Sprintf(`zigbee_voltage_volts{%s}`, labels), value, true
		case "mV":
			return fmt.Sprintf(`zigbee_voltage_volts{%s}`, labels), value / 1000, true
		}
	case "battery":
		if e.Unit == "" || e.Unit == "%" {
			return fmt.Sprintf(`zigbee_battery_percent{%s}`, labels), value, true
		}
	case "linkquality":
		return fmt.Sprintf(`zigbee_linkquality{%s}`, labels), value, true
	}
	return "", 0, false
}

type pendingState struct {
	fields     map[string]any
	receivedAt time.Time
}

type monitor struct {
	mu            sync.Mutex
	devices       map[string]deviceInfo        // friendly_name -> info
	online        map[string]bool              // friendly_name -> is online
	values        map[string]map[string]string // friendly_name -> property -> metric name
	pending       map[string]pendingState
	hasDeviceList bool
}

func (m *monitor) handleDeviceList(payload json.RawMessage) {
	var devices []deviceInfo
	if err := json.Unmarshal(payload, &devices); err != nil {
		log.Printf("failed to parse device list: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	old := m.devices
	next := make(map[string]deviceInfo)
	for _, d := range devices {
		if d.Type == "Coordinator" {
			continue
		}
		next[d.FriendlyName] = d
	}

	for name, info := range old {
		newInfo, exists := next[name]
		if !exists || newInfo.IEEEAddress != info.IEEEAddress {
			metrics.UnregisterMetric(deviceMetricName(name, info.IEEEAddress))
			metrics.UnregisterMetric(lastMessageMetricName(name, info.IEEEAddress))
			for _, metricName := range m.values[name] {
				metrics.UnregisterMetric(metricName)
			}
			delete(m.values, name)
			delete(m.online, name)
		}
	}
	m.devices = next
	m.hasDeviceList = true

	for name, info := range m.devices {
		online, known := m.online[name]
		var value float64
		if known && online {
			value = 1
		}
		metrics.GetOrCreateGauge(deviceMetricName(name, info.IEEEAddress), nil).Set(value)
	}
	for name, state := range m.pending {
		if _, exists := m.devices[name]; exists {
			m.applyStateLocked(name, state.fields, state.receivedAt)
		}
	}
	clear(m.pending)

	log.Printf("device list updated: %d devices", len(m.devices))
}

func (m *monitor) applyStateLocked(name string, state map[string]any, receivedAt time.Time) {
	info := m.devices[name]
	if m.values == nil {
		m.values = make(map[string]map[string]string)
	}
	if m.values[name] == nil {
		m.values[name] = make(map[string]string)
	}
	for property, raw := range state {
		oldMetric := m.values[name][property]
		metricName, value, valid := metricForValue(info, property, raw)
		if oldMetric != "" && oldMetric != metricName {
			metrics.UnregisterMetric(oldMetric)
		}
		if valid {
			metrics.GetOrCreateGauge(metricName, nil).Set(value)
			m.values[name][property] = metricName
		} else {
			delete(m.values[name], property)
		}
	}
	metrics.GetOrCreateGauge(lastMessageMetricName(name, info.IEEEAddress), nil).Set(float64(receivedAt.Unix()))
}

func (m *monitor) handleDeviceState(name string, payload json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, known := m.devices[name]
	if m.hasDeviceList && !known {
		return
	}

	var state map[string]any
	if err := json.Unmarshal(payload, &state); err != nil || state == nil {
		// Before the inventory arrives, non-object payloads may be attribute replays.
		if known {
			log.Printf("failed to parse state for %s: %v", name, err)
		}
		return
	}
	if !m.hasDeviceList {
		if m.pending == nil {
			m.pending = make(map[string]pendingState)
		}
		pending := m.pending[name]
		if pending.fields == nil {
			pending.fields = make(map[string]any)
		}
		maps.Copy(pending.fields, state)
		pending.receivedAt = time.Now()
		m.pending[name] = pending
		return
	}
	m.applyStateLocked(name, state, time.Now())
}

func (m *monitor) resetForConnection() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, info := range m.devices {
		metrics.GetOrCreateGauge(deviceMetricName(name, info.IEEEAddress), nil).Set(0)
		metrics.UnregisterMetric(lastMessageMetricName(name, info.IEEEAddress))
		for _, metricName := range m.values[name] {
			metrics.UnregisterMetric(metricName)
		}
	}
	m.online = make(map[string]bool)
	m.values = make(map[string]map[string]string)
	m.pending = make(map[string]pendingState)
	m.hasDeviceList = false
}

func (m *monitor) handleAvailability(name string, payload json.RawMessage) {
	var avail struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(payload, &avail); err != nil {
		log.Printf("failed to parse availability for %s: %v", name, err)
		return
	}

	online := avail.State == "online"

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.devices[name]; !exists && m.hasDeviceList {
		return
	}
	if m.online == nil {
		m.online = make(map[string]bool)
	}

	prev, known := m.online[name]
	m.online[name] = online

	if known && prev != online {
		log.Printf("device %s: %s -> %s", name, boolToState(prev), boolToState(online))
	} else if !known {
		log.Printf("device %s: %s", name, boolToState(online))
	}

	if info, exists := m.devices[name]; exists {
		var value float64
		if online {
			value = 1
		}
		metrics.GetOrCreateGauge(deviceMetricName(name, info.IEEEAddress), nil).Set(value)
	}
}

func boolToState(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}

func connect(wsURL string, m *monitor) error {
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	m.resetForConnection()
	setConnectionMetric(true)
	defer setConnectionMetric(false)
	c.SetReadLimit(10 * 1024 * 1024)

	log.Printf("connected to %s", wsURL)

	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var msg struct {
			Topic   string          `json:"topic"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("parse error: %v", err)
			continue
		}

		switch {
		case msg.Topic == "bridge/devices":
			m.handleDeviceList(msg.Payload)
		case strings.HasSuffix(msg.Topic, "/availability"):
			name := strings.TrimSuffix(msg.Topic, "/availability")
			if name != "bridge" {
				m.handleAvailability(name, msg.Payload)
			}
		case msg.Topic != "" && !strings.HasPrefix(msg.Topic, "bridge/") &&
			!strings.HasSuffix(msg.Topic, "/action") &&
			!strings.HasSuffix(msg.Topic, "/set") &&
			!strings.HasSuffix(msg.Topic, "/get"):
			m.handleDeviceState(msg.Topic, msg.Payload)
		}
	}
}

func main() {
	z2mAddr := flag.String("z2m", "ws://localhost:8080/api", "Zigbee2MQTT WebSocket API address")
	metricsPort := flag.Int("port", 9099, "Prometheus metrics port")
	flag.Parse()

	m := &monitor{
		devices: make(map[string]deviceInfo),
		online:  make(map[string]bool),
		values:  make(map[string]map[string]string),
		pending: make(map[string]pendingState),
	}
	setConnectionMetric(false)

	go func() {
		http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.WritePrometheus(w, false)
		})
		log.Printf("serving metrics on :%d", *metricsPort)
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *metricsPort), nil))
	}()

	for {
		if err := connect(*z2mAddr, m); err != nil {
			log.Printf("connection error: %v, retrying in 10s", err)
		}
		time.Sleep(10 * time.Second)
	}
}
