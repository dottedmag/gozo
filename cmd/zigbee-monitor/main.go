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

type deviceInfo struct {
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	IEEEAddress  string `json:"ieee_address"`
}

func deviceMetricName(name, ieee string) string {
	return fmt.Sprintf(`zigbee_device_up{name=%q, ieee=%q}`, name, ieee)
}

type monitor struct {
	mu      sync.Mutex
	devices map[string]deviceInfo // friendly_name -> info
	online  map[string]bool      // friendly_name -> is online
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
	m.devices = make(map[string]deviceInfo)
	for _, d := range devices {
		if d.Type == "Coordinator" {
			continue
		}
		m.devices[d.FriendlyName] = d
	}

	for name, info := range old {
		if _, exists := m.devices[name]; !exists {
			metrics.UnregisterMetric(deviceMetricName(name, info.IEEEAddress))
		}
	}

	for name, info := range m.devices {
		online, known := m.online[name]
		var value float64
		if known && online {
			value = 1
		}
		metrics.GetOrCreateGauge(deviceMetricName(name, info.IEEEAddress), nil).Set(value)
	}

	log.Printf("device list updated: %d devices", len(m.devices))
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
	}

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
