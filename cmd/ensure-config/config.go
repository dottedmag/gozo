package main

import (
	"fmt"
	"strconv"
)

type config struct {
	ZWaveJSAPIEndpoint string             `toml:"zwavejs_api_endpoint"`
	DeviceTypes        []configDeviceType `toml:"device_type"`
	Nodes              []configNode       `toml:"node"`
}

type configDeviceType struct {
	Name        string
	Description string
	Params      []configDeviceTypeParam
}

type configDeviceTypeParam struct {
	// FIXME (dottedmag): Support "sub-parameters"
	ID          int
	Description string
	Default     *uint   `toml:"default"`
	DefaultHex  *string `toml:"default_hex"`
}

type configNode struct {
	ID          int
	DeviceType  string `toml:"device_type"`
	Description string
	Params      []configNodeParam
}

type configNodeParam struct {
	ID    int
	Value *int
}

func parseConfig(c config) (map[int]node, error) {
	out := map[int]node{}

	dts := map[string]deviceType{}
	for _, dt := range c.DeviceTypes {
		if _, ok := dts[dt.Name]; ok {
			return nil, fmt.Errorf("device type %s: duplicate", dt.Name)
		}
		dts[dt.Name] = deviceType{paramsDefaultValues: map[int]*uint{}, paramsDescriptions: map[int]string{}}

		for _, param := range dt.Params {
			switch {
			case param.Default != nil && param.DefaultHex != nil:
				return nil, fmt.Errorf("device type %s: parameter %d has both default and defaultHex values", dt.Name, param.ID)
			case param.Default != nil:
				dts[dt.Name].paramsDefaultValues[param.ID] = param.Default
			case param.DefaultHex != nil:
				u, err := strconv.ParseUint(*param.DefaultHex, 16, 32)
				if err != nil {
					return nil, fmt.Errorf("device type %s: parameter %d: defaultHex value %s is not a valid hex number", dt.Name, param.ID, *param.DefaultHex)
				}
				v := uint(u)
				dts[dt.Name].paramsDefaultValues[param.ID] = &v
			}
			dts[dt.Name].paramsDescriptions[param.ID] = param.Description
		}

	}

	for _, cn := range c.Nodes {
		if _, ok := out[cn.ID]; ok {
			return nil, fmt.Errorf("node %d is present multiple times in config", cn.ID)
		}
		dt, ok := dts[cn.DeviceType]
		if !ok {
			return nil, fmt.Errorf("node %d: device type %s is not defined", cn.ID, cn.DeviceType)
		}

		params := map[int]param{}
		for _, cp := range cn.Params {
			if _, ok := params[cp.ID]; ok {
				return nil, fmt.Errorf("parameter %d for node %d is present multiple times in config", cp.ID, cn.ID)
			}
			if dt.paramsDescriptions[cp.ID] == "" {
				return nil, fmt.Errorf("parameter %d for node %d is not defined in device type %s", cp.ID, cn.ID, cn.DeviceType)
			}
			if cp.Value == nil {
				return nil, fmt.Errorf("parameter %d for node %d has no value in config", cp.ID, cn.ID)
			}

			params[cp.ID] = param{description: dt.paramsDescriptions[cp.ID], value: uint(*cp.Value)}
		}

		for id, v := range dt.paramsDefaultValues {
			if params[id].description != "" {
				continue
			}
			params[id] = param{description: dt.paramsDescriptions[id], value: *v}
		}

		out[cn.ID] = node{description: cn.Description, params: params}
	}

	return out, nil
}
