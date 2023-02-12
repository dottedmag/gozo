# A collection of Z-Wave tools

All these tools use [zwave-js](https://github.com/zwave-js) WebSocket API.

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

# Legal

Copyright 2023 Mikhail Gusarov.

Licensed under [Apache 2.0 license](LICENSE-2.0.txt).
