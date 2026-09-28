# Home automation tools

The Z-Wave tools use the [zwave-js](https://github.com/zwave-js) WebSocket API.

This API is available via
- [Z-Wave JS UI](https://github.com/zwave-js/zwave-js-ui)
- standalone [zwave-js-server](https://github.com/zwave-js/zwave-js-server)

## schedule

Turns Z-Wave nodes off and on on schedule. See [the example config](cmd/schedule/config.toml.example).

## schedule-thermostat

Switches thermostats off and on on schedule. See [the example config](cmd/schedule-thermostat/config.toml.example).

## ensure-config

Periodically refreshes Z-Wave node configurations.

Useful for making network configuration declarative, and for resetting nodes
that lose their configuration after power loss.

See [the example config](cmd/ensure-config/config.toml.example).

## zwave-monitor

Exports Z-Wave JS readings at `http://localhost:9098/metrics` (change the address
with `-zwavejs` and the HTTP port with `-port`). It reads the initial node state
and follows value events, so metrics appear as soon as known values are reported.

- `zwave_device_up`: node availability.
- `zwave_monitor_connected`: 1 while the WebSocket connection to Z-Wave JS is active, 0 otherwise.
- `zwave_node_last_value_update_timestamp_seconds`: Unix time of the last value event observed for a node. It appears after the first live value event; cached values in the initial state do not establish a report time.
- `zwave_switch_on`: actual Binary Switch state, by endpoint (1 on, 0 off).
- `zwave_power_watts`: instantaneous Meter readings in watts from switches and thermostats. `endpoint` and `property_key` distinguish meter channels.
- `zwave_energy_kwh`: cumulative Meter energy, including Wh readings converted to kWh.
- `zwave_voltage_volts`: Meter voltage readings where reported.
- `zwave_temperature_celsius`: Multilevel Sensor temperatures from the CO2 monitor and every thermostat probe. Fahrenheit readings are converted to Celsius; `endpoint` distinguishes the Z-TRM3 probes.
- `zwave_temperature_setpoint_celsius`: thermostat target temperatures, with `setpoint` identifying the mode.
- `zwave_thermostat_mode`: Z-Wave thermostat mode code (Z-TRM3: 0 off, 1 heat).
- `zwave_thermostat_heating`: Z-TRM3 operating state (1 heating, 0 idle).
- `zwave_humidity_percent`: relative humidity from the CO2 monitor.
- `zwave_co2_ppm`: carbon dioxide concentration from the CO2 monitor.

All node series include `node_id` and `name`. A value is removed when Z-Wave JS
removes it or its node. Readings retain their last value during a disconnection;
use `zwave_monitor_connected` to detect that case. The Z-TRM3 may report `0`
for an unconnected external temperature probe.

## zigbee-monitor

Exports Zigbee2MQTT readings at `http://localhost:9099/metrics` (change the
WebSocket address with `-z2m` and the HTTP port with `-port`). It uses the
`bridge/devices` expose definitions to identify device properties, including
properties with endpoint suffixes.

- `zigbee_device_up`: device availability.
- `zigbee_monitor_connected`: 1 while connected to the Zigbee2MQTT WebSocket, 0 otherwise.
- `zigbee_device_last_message_timestamp_seconds`: Unix time when the monitor received the last device state message, including a cached message replayed on connection.
- `zigbee_temperature_celsius` and `zigbee_humidity_percent`: sensor readings.
- `zigbee_light_on` and `zigbee_brightness_level`: dimmer state (1 on, 0 off) and raw Zigbee2MQTT brightness level.
- `zigbee_power_watts`, `zigbee_energy_kwh`, and `zigbee_voltage_volts`: exported when a device reports them. Wh and mV are converted to kWh and V.
- `zigbee_battery_percent` and `zigbee_linkquality`: sensor diagnostics when reported.

Device readings include `name`, `ieee`, and the Zigbee2MQTT `property` so
endpoint values remain distinct. Readings retain their last value during a
disconnection; they are cleared when the monitor reconnects and waits for
fresh or replayed device state.

# Legal

Copyright 2023 Mikhail Gusarov.

Licensed under [Apache 2.0 license](LICENSE-2.0.txt).
