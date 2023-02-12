package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"github.com/dottedmag/gozo"
	"github.com/ridge/must/v2"
	"golang.org/x/term"
)

func getKey() string {
	for {
		var b [10]byte
		n, err := os.Stdin.Read(b[:])
		if err != nil {
			panic(err)
		}

		for i := 0; i < n; i++ {
			if b[0] == 0x03 {
				os.Exit(0)
			}
			if b[0] == 0x1b {
				if bytes.HasPrefix(b[1:], []byte{'[', 'A'}) {
					return "up"
				}
				if bytes.HasPrefix(b[1:], []byte{'[', 'B'}) {
					return "down"
				}
				if bytes.HasPrefix(b[1:], []byte{'[', '1', ';', '2', 'A'}) {
					return "shift-up"
				}
				if bytes.HasPrefix(b[1:], []byte{'[', '1', ';', '2', 'B'}) {
					return "shift-down"
				}
			}
		}
	}
}

func setLevel(c *gozo.Conn, node int, level int) {
	_, err := c.Call("node.set_value", map[string]any{
		"nodeId": node,
		"valueId": map[string]any{
			"commandClass": 0x26, // Multilevel Switch CC
			"property":     "targetValue",
		},
		"value": level,
	})
	if err != nil {
		panic(err)
	}

	for {
		curLevel := getLevel(c, node)
		fmt.Printf("<- %d\r\n", curLevel)
		if curLevel == level {
			break
		}
	}
}

func getLevel(c *gozo.Conn, node int) int {
	resp, err := c.Call("node.get_value", map[string]any{
		"nodeId": node,
		"valueId": map[string]any{
			"commandClass": 0x26,
			"property":     "currentValue",
		},
	})
	if err != nil {
		panic(err)
	}
	return int(resp["result"].(map[string]any)["value"].(float64))
}

func main() {
	id := int(must.OK1(strconv.ParseInt(os.Args[2], 10, 64)))

	c, err := gozo.NewConn(os.Args[1], func(m map[string]interface{}) {})
	if err != nil {
		panic(err)
	}

	termState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), termState)

	level := 50

	for {
		fmt.Printf("%d\r\n", level)
		setLevel(c, id, level)

		switch getKey() {
		case "up":
			level += 10
		case "down":
			level -= 10
		case "shift-up":
			level += 1
		case "shift-down":
			level -= 1
		}

		if level < 0 {
			level = 0
		}
		if level > 99 {
			level = 99
		}
	}
}
