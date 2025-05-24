package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dottedmag/must"
	"github.com/dottedmag/tj"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

const z2mBase = "zigbee2mqtt/"

type dimmerState struct {
	State         string `json:"state"` // "" on startup, "ON"/"OFF" then
	MinBrightness int    `json:"min_brightness"`
	Brightness    int    `json:"brightness"`
	MaxBrightness int    `json:"max_brightness"`
}

func realMain() int {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: relay <mqtt address> <relayer>:<relayee> [<relayer>:<relayee>...]\n")
		return 2
	}

	addr := os.Args[1]
	controllers := map[string]string{}
	dimmers := map[string]*dimmerState{}

	for _, arg := range os.Args[2:] {
		from, to, ok := strings.Cut(arg, ":")
		if !ok {
			fmt.Fprintf(os.Stderr, "Failed to parse %q as controller:controllee\n", arg)
			return 2
		}
		controllers[from] = to
		dimmers[to] = &dimmerState{}
	}

	router := paho.NewStandardRouter()

	cliCfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{must.OK1(url.Parse(addr))},
		KeepAlive:                     20,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		OnConnectError: func(err error) {
			fmt.Printf("error whilst attempting connection: %s\n", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: "relay",
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
			for dimmer := range dimmers {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: z2mBase + dimmer})
			}
			for controller := range controllers {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: z2mBase + controller + "/action"})
			}
			must.OK1(cm.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: subscriptions}))
		},
	}

	ctx := context.Background()
	c := must.OK1(autopaho.NewConnection(ctx, cliCfg))
	must.OK(c.AwaitConnection(ctx))

	brightnessChange := func(dimmer string, delta int) {
		mb := dimmers[dimmer].MaxBrightness
		if mb == 0 {
			mb = 255
		}
		nextBrightness := min(mb, max(dimmers[dimmer].MinBrightness, dimmers[dimmer].Brightness+delta))
		must.OK1(c.Publish(ctx, &paho.Publish{
			Topic:   z2mBase + dimmer + "/set",
			Payload: must.OK1(json.Marshal(tj.O{"state": "ON", "brightness": nextBrightness})),
		}))
		// Optimistically update brightness in the struct, so that subsequent rotation messages don't race with updates
		// from the idmmer
		dimmers[dimmer].Brightness = nextBrightness
	}

	toggle := func(dimmer string) {
		var nextState = "ON"
		if dimmers[dimmer].State == "ON" {
			nextState = "OFF"
		}

		must.OK1(c.Publish(ctx, &paho.Publish{
			Topic:   z2mBase + dimmer + "/set",
			Payload: must.OK1(json.Marshal(tj.O{"state": nextState})),
		}))
	}

	for dimmer := range dimmers {
		router.RegisterHandler(z2mBase+dimmer, func(p *paho.Publish) {
			must.OK(json.Unmarshal(p.Payload, dimmers[dimmer]))
		})
	}
	for controller, dimmer := range controllers {
		router.RegisterHandler(z2mBase+controller+"/action", func(p *paho.Publish) {
			if dimmers[dimmer].State == "" { // haven't heard from dimmer yet
				return
			}
			switch string(p.Payload) {
			case "single": // click
				toggle(dimmer)
			case "rotate_left":
				brightnessChange(dimmer, -10)
			case "rotate_right":
				brightnessChange(dimmer, 10)
			}
		})
	}

	<-c.Done()
	return 0
}

func main() {
	os.Exit(realMain())
}
