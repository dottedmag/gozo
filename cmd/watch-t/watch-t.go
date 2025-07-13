package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dottedmag/must"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

const (
	z2mBase   = "zigbee2mqtt/"
	zwaveBase = "zwave/"
)

func realMain() int {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: watch-t zigbee:name zwave:loc:n:t...\n")
		return 2
	}

	mqttAddr := os.Args[1]

	zigbeeTs := map[string]bool{}
	zwaveTs := map[string]bool{}

	for _, arg := range os.Args[2:] {
		if suffix, ok := strings.CutPrefix(arg, "zigbee:"); ok {
			zigbeeTs[suffix] = true
		} else if suffix, ok := strings.CutPrefix(arg, "zwave:"); ok {
			parts := strings.Split(suffix, ":")
			p := parts[0] + "/nodeID_" + parts[1] + "/sensor_multilevel/endpoint_" + parts[2] + "/Air_temperature"
			zwaveTs[p] = true
		} else {
			panic("expected zwave: zigbee:")
		}
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
			ClientID: "watch-t",
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
			for name := range zigbeeTs {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: z2mBase + name})
			}
			for name := range zwaveTs {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: zwaveBase + name})
			}
			must.OK1(cm.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: subscriptions}))
		},
	}

	ctx := context.Background()
	c := must.OK1(autopaho.NewConnection(ctx, cliCfg))
	must.OK(c.AwaitConnection(ctx))

	for name := range zigbeeTs {
		name := name // rm when loopvar changes to default
		router.RegisterHandler(z2mBase+name, func(p *paho.Publish) {
			var d struct {
				Temperature float64
			}
			must.OK(json.Unmarshal(p.Payload, &d))
			fmt.Printf("%s: %.1f\n", name, d.Temperature)
		})
	}
	for name := range zwaveTs {
		name := name // rm when loopvar changes to default
		router.RegisterHandler(zwaveBase+name, func(p *paho.Publish) {
			var d struct {
				Value float64
			}
			must.OK(json.Unmarshal(p.Payload, &d))
			fmt.Printf("%s: %.1f\n", name, d.Value)
		})
	}

	<-c.Done()
	return 0
}

func main() {
	os.Exit(realMain())
}
