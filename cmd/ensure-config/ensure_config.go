package main

import (
	"log"
	"os"
	"time"

	"github.com/dottedmag/gozo"
	"github.com/pelletier/go-toml/v2"
)

type deviceType struct {
	paramsDescriptions  map[int]string
	paramsDefaultValues map[int]*uint
}

type param struct {
	description string
	value       uint
}

type node struct {
	id          int
	description string
	params      map[int]param
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

	var config config
	dec := toml.NewDecoder(fh)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&config); err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	nodes, err := parseConfig(config)
	if err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	c, err := gozo.NewConn(config.ZWaveJSAPIEndpoint, func(m map[string]interface{}) {})
	if err != nil {
		// TODO (dottedmag): Handle zwave-js API endpoint reconnections
		log.Printf("FATAL: Failed to connect to zwave-js API endpoint %s: %v", config.ZWaveJSAPIEndpoint, err)
		os.Exit(1)
	}

	for id, node := range nodes {
		log.Printf("INFO: Servicing node %d (%s)", id, node.description)
	}

	for {
		var anyFailed, anyDead bool

		for id, node := range nodes {
			log.Printf("INFO: Handling node %d", id)

			resp, err := c.Call("node.get_state", map[string]any{
				"nodeId": id,
			})

			if err != nil {
				log.Printf("ERR: failed to query state of node %d (%s): %v", id, node.description, err)
				anyFailed = true
				continue
			}

			if resp["success"] == nil || !resp["success"].(bool) {
				log.Printf("ERR: failed to query state of node %d (%s): %v", id, node.description, resp)
				anyFailed = true
				continue
			}

			if resp["result"].(map[string]any)["state"].(map[string]any)["status"].(float64) == 3 {
				log.Printf("INFO: Node %d (%s) is dead", id, node.description)
				anyDead = true
				continue
			}

			for n, param := range node.params {
				resp, err := c.Call("node.get_value", map[string]any{
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

				if resp["success"] == nil || !resp["success"].(bool) {
					log.Printf("ERR: Failed to obtain current value %d (%s) %d (%s): %#v", id, node.description, n, param.description, resp)
					anyFailed = true
					continue
				}

				// fmt.Printf("nodeId=%d property=%d %#v\n", id, n, resp)

				anyValue := resp["result"].(map[string]any)["value"]
				if anyValue == nil {
					log.Printf("ERR: Empty current value %d (%s) %d (%s): %#v", id, node.description, n, param.description, resp)
					anyFailed = true
					continue
				}

				value := uint(anyValue.(float64))

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
			}

			time.Sleep(10 * time.Second) // Some delay between nodes to avoid hogging bandwith

		}

		if anyFailed {
			time.Sleep(10 * time.Second)
		} else if anyDead {
			time.Sleep(5 * time.Minute)
		} else {
			// TODO (dottedmag): Increase precision of scheduling
			time.Sleep(5 * time.Minute)
		}
	}
}
