package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/dottedmag/gozo"
)

type config struct {
	ZWaveJSAPIEndpoint string
	Nodes              []configNode
}

type configNode struct {
	ID          int
	Description string
	Params      []configParam
}

type configParam struct {
	// TODO (dottedmag): Support "sub-parameters"
	Number      int
	IntValue    *uint   `json:"int_value"`
	HexValue    *string `json:"hex_value"`
	Description string
}

type node struct {
	description string
	params      map[int]param
}

type param struct {
	description string
	value       uint
}

func parseConfig(c config) (map[int]node, error) {
	out := map[int]node{}

	for _, cn := range c.Nodes {
		if _, ok := out[cn.ID]; ok {
			return nil, fmt.Errorf("node %d is present multiple times in config", cn.ID)
		}
		params := map[int]param{}
		for _, cp := range cn.Params {
			if _, ok := params[cp.Number]; ok {
				return nil, fmt.Errorf("parameter %d for node %d is present multiple times in config", cp.Number, cn.ID)
			}
			switch {
			case cp.IntValue != nil && cp.HexValue != nil:
				return nil, fmt.Errorf("parameter %d for node %d has both int and hex values", cp.Number, cn.ID)
			case cp.IntValue != nil:
				params[cp.Number] = param{description: cp.Description, value: *cp.IntValue}
			case cp.HexValue != nil:
				u, err := strconv.ParseUint(*cp.HexValue, 16, 32)
				if err != nil {
					return nil, fmt.Errorf("parameter %d for node %d hex value %q is malformed: %v", cp.Number, cn.ID, *cp.HexValue, err)
				}
				params[cp.Number] = param{description: cp.Description, value: uint(u)}
			default:
				return nil, fmt.Errorf("parameter %d for node %d does not have any value in config", cp.Number, cn.ID)
			}
		}
		out[cn.ID] = node{description: cn.Description, params: params}
	}

	return out, nil
}

func main() {
	log.SetFlags(log.LUTC)

	if len(os.Args) != 2 {
		log.Printf("Usage: ensure-config <config-file>")
		os.Exit(2)
	}

	fh, err := os.Open(os.Args[1])
	if err != nil {
		log.Printf("FATAL: Failed to open config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	dec := json.NewDecoder(fh)
	dec.DisallowUnknownFields()

	var config config
	if err := dec.Decode(&config); err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	nodes, err := parseConfig(config)
	if err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	c, err := gozo.NewConn(config.ZWaveJSAPIEndpoint)
	if err != nil {
		// TODO (dottedmag): Handle zwave-js API endpoint reconnections
		log.Printf("FATAL: Failed to connect to zwave-js API endpoint %s: %v", config.ZWaveJSAPIEndpoint, err)
		os.Exit(1)
	}

	for id, node := range nodes {
		log.Printf("INFO: Servicing node %d (%s)", id, node.description)
	}

	for {
		var anyFailed bool

		for id, node := range nodes {
			for n, param := range node.params {
				resp, err := c.Call("node.poll_value", map[string]any{
					"nodeId": id,
					"valueId": map[string]any{
						"commandClass": 0x70, // Configuration CC
						"property":     n,
					},
				})

				if err != nil {
					log.Printf("ERR: Failed to obtain current value %d (%s) %d (%s): %v", id, node.description, n, param.description, err)
					anyFailed = true
					continue
				}

				// TODO (dottedmag): Recongnize "node is offline", and use different scheduling algorithm
				// (offline nodes are likely to stay offline for a while, as they are probably just unplugged)
				if resp["success"] == nil || !resp["success"].(bool) {
					log.Printf("ERR: Failed to obtain current value %d (%s) %d (%s): %#v", id, node.description, n, param.description, resp)
					anyFailed = true
					continue
				}

				value := uint(resp["result"].(map[string]any)["value"].(float64))

				if value != param.value {
					resp, err := c.Call("node.set_value", map[string]any{
						"nodeId": id,
						"valueId": map[string]any{
							"commandClass": 0x70, // Configuration CC
							"property":     n,
						},
						"value": param.value,
					})

					if err != nil {
						log.Printf("ERR: Failed to set value %d (%s) %d (%s) %d->%d: %v", id, node.description, n, param.description, value, param.value, err)
						anyFailed = true
						continue
					}

					// TODO (dottedmag): Recongnize "node is offline", and use different scheduling algorithm
					// (offline nodes are likely to stay offline for a while, as they are probably just unplugged)
					if resp["success"] == nil || !resp["success"].(bool) {
						log.Printf("ERR: Failed to set value %d (%s) %d (%s) %d->%d: %#v", id, node.description, n, param.description, value, param.value, resp)
						anyFailed = true
						continue
					}

					log.Printf("INFO: Set value %d (%s) %d (%s) %v->%v", id, node.description, n, param.description, value, param.value)
				}

				time.Sleep(2 * time.Second) // Some delay between parameters to avoid hogging bandwith
			}

			time.Sleep(10 * time.Second) // Some delay between nodes to avoid hogging bandwith

		}

		if anyFailed {
			time.Sleep(10 * time.Second)
		} else {
			// TODO (dottedmag): Increase precision of scheduling
			time.Sleep(5 * time.Minute)
		}
	}
}
