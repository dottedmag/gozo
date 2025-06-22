package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dottedmag/gozo"
	"github.com/dottedmag/must"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

const z2mBase = "zigbee2mqtt/"

func realMain() int {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: relay-level-cross <mqtt address> <zwave-js API address> <relayer>:<relayee> [<relayer>:<relayee>...]\n")
		return 2
	}

	mqttAddr := os.Args[1]
	zwaveJSAPIAddr := os.Args[2]
	controllers := map[string]int{}

	for _, arg := range os.Args[3:] {
		from, to, ok := strings.Cut(arg, ":")
		if !ok {
			fmt.Fprintf(os.Stderr, "Failed to parse %q as controller:controllee\n", arg)
			return 2
		}
		toInt := must.OK1(strconv.Atoi(to))
		controllers[from] = toInt
	}

	zc, err := gozo.NewConn(zwaveJSAPIAddr, func(m map[string]any) {})
	if err != nil {
		// TODO (dottedmag): Handle zwave-js API endpoint reconnections
		log.Printf("FATAL: Failed to connect to zwave-js API endpoint %s: %v", zwaveJSAPIAddr, err)
		os.Exit(1)
	}

	router := paho.NewStandardRouter()

	cliCfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{must.OK1(url.Parse(mqttAddr))},
		KeepAlive:                     20,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		OnConnectError: func(err error) {
			fmt.Printf("error whilst attempting connection: %s\n", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: "relay-cross",
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					router.Route(pr.Packet.Packet())
					return true, nil
				},
			},
			OnClientError: func(err error) {
				fmt.Printf("client error: %s\n", err)
			},
			OnServerDisconnect: func(d *paho.Disconnect) {
				if d.Properties != nil {
					fmt.Printf("server requested disconnect: %s\n", d.Properties.ReasonString)
				} else {
					fmt.Printf("server requested disconnect; reason code: %d\n", d.ReasonCode)
				}
			},
		},
		OnConnectionUp: func(cm *autopaho.ConnectionManager, connack *paho.Connack) {
			var subscriptions []paho.SubscribeOptions
			for controller := range controllers {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: z2mBase + controller + "/action"})
			}
			must.OK1(cm.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: subscriptions}))
		},
	}

	ctx := context.Background()
	c := must.OK1(autopaho.NewConnection(ctx, cliCfg))
	must.OK(c.AwaitConnection(ctx))

	toggle := func(nodeID int) {
		fmt.Printf("%s received toggle event\n", time.Now().Format(time.RFC3339Nano))
		resp, err := zc.Call("node.get_value", map[string]any{
			"nodeId": nodeID,
			"valueId": map[string]any{
				"commandClass": 37, // Binary Switch CC
				"endpoint":     0,  // default, TODO: extend?
				"property":     "currentValue",
			},
		})
		if err != nil {
			fmt.Printf("Failed to obtain state of a switch %d: %v\n", nodeID, err)
			return
		}
		fmt.Printf("%s received current value\n", time.Now().Format(time.RFC3339Nano))

		if resp["success"] == nil || !resp["success"].(bool) {
			log.Printf("ERR: Failed to obtain current value of a switch %d: %#v", nodeID, resp)
		}

		value := resp["result"].(map[string]any)["value"].(bool)
		newValue := !value

		fmt.Printf("%s sending new value\n", time.Now().Format(time.RFC3339Nano))
		resp, err = zc.Call("node.set_value", map[string]any{
			"nodeId": nodeID,
			"valueId": map[string]any{
				"commandClass": 37, // Configuration CC
				"endpoint":     0,  // default, TODO:extend?
				"property":     "targetValue",
			},
			"value": newValue,
		})
		fmt.Printf("%s sent new value\n", time.Now().Format(time.RFC3339Nano))
	}

	for controller, nanoswitch := range controllers {
		controller, nanoswitch := controller, nanoswitch // rm when loopvar changes to default
		router.RegisterHandler(z2mBase+controller+"/action", func(p *paho.Publish) {
			switch string(p.Payload) {
			case "single": // click
				toggle(nanoswitch)
			}
		})
	}

	<-c.Done()
	return 0
}

func main() {
	os.Exit(realMain())
}
