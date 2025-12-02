# Grafana RRD Server

A simple HTTP server that reads RRD files and responds to requests from Grafana via JSON API.

[![CircleCI](https://img.shields.io/circleci/project/github/doublemarket/grafana-rrd-server.svg)](https://circleci.com/gh/doublemarket/grafana-rrd-server)
[![Coveralls](https://img.shields.io/coveralls/doublemarket/grafana-rrd-server.svg)](https://coveralls.io/github/doublemarket/grafana-rrd-server)
[![GitHub release](https://img.shields.io/github/release/doublemarket/grafana-rrd-server.svg)](https://github.com/doublemarket/grafana-rrd-server/releases)

**Recommended:** Use with [Grafana Infinity Datasource](https://grafana.com/grafana/plugins/yesoreyeram-infinity-datasource/) (maintained by Grafana Labs) for JSON API support.

**Legacy:** Also compatible with the deprecated [Simple JSON Datasource plugin](https://grafana.net/plugins/grafana-simple-json-datasource).

This server implements the Simple JSON datasource API endpoints with the following features:

- **Wildcard support**: You can use `*` as a wildcard in the `target` values (but not for `ds`) for the `/query` endpoint.
- **RRDCached support**: Hybrid mode with automatic fallback - uses rrdcached when available, direct file access otherwise
- **Directory browsing**: `/ls` endpoint for hierarchical RRD file discovery
- **Flexible search**: `/search` endpoint with substring matching across all metrics

## Features

- Structured JSON logging with `log/slog`
- Graceful shutdown handling (SIGINT, SIGTERM)
- HTTP server with configurable timeouts
- Concurrent search cache updates
- Proper error handling and HTTP status codes

## API Endpoints

### `/` - Health Check
Returns a simple JSON response to verify the server is running.

```bash
curl http://localhost:9000/
# {"message":"hello"}
```

### `/ls` - Directory Listing
Browse RRD files hierarchically. Returns directories and files at the specified path level.

```bash
# List root directory
curl -X POST http://localhost:9000/ls -H "Content-Type: application/json" -d '{"target":""}'
# or simply:
curl -X POST http://localhost:9000/ls

# List subdirectory
curl -X POST http://localhost:9000/ls -H "Content-Type: application/json" -d '{"target":"hostname1"}'

# Response format:
{
  "directories": ["subdir1", "subdir2"],
  "files": ["file1", "file2"]
}
```

### `/search` - Metric Search
Search for metrics across all RRD files. Returns metrics matching the substring.

```bash
# Search for all metrics containing "cpu"
curl -X POST http://localhost:9000/search \
  -H "Content-Type: application/json" \
  -d '{"target":"cpu"}'

# Search for all datasources in a specific file
curl -X POST http://localhost:9000/search \
  -H "Content-Type: application/json" \
  -d '{"target":"percent-user:"}'

# Response: ["percent-user:value", "percent-idle:value"]
```

### `/query` - Time Series Data
Query time series data from RRD files.

```bash
curl -X POST http://localhost:9000/query \
  -H "Content-Type: application/json" \
  -d '{
    "range": {
      "from": "2016-12-07T22:47:00Z",
      "to": "2016-12-08T02:08:00Z"
    },
    "targets": [
      {
        "target": "percent-user:value",
        "refId": "A",
        "type": "timeseries"
      }
    ]
  }'

# Response format:
[
  {
    "target": "percent-user:value",
    "datapoints": [[value, timestamp_ms], ...]
  }
]
```

### `/annotations` - Event Annotations
Query annotations from CSV file (if configured with `-a` flag).

## Metric Naming Convention

Metrics follow the pattern: `path:to:file:datasource`

- File paths are converted: `/` → `:`
- Example: `./rrd/host1/port-eth0.rrd` with datasource `traffic_in` becomes `rrd:host1:port-eth0:traffic_in`
- Subdirectories are preserved in the colon-separated path

# Requirements

- librrd-dev (rrdtool)
- Go 1.23 or later
- Grafana 10.4.8 or newer
- Recommended: [Infinity Datasource plugin](https://grafana.com/grafana/plugins/yesoreyeram-infinity-datasource/) (actively maintained)
- Legacy: Simple JSON Datasource plugin 1.0.0+ (deprecated)

# Usage

1. Install librrd-dev (rrdtool).

   On Ubuntu/Debian:

   ```bash
   sudo apt install librrd-dev
   ```

   On CentOS/RHEL:

   ```bash
   sudo yum install rrdtool-devel
   ```

   On openSUSE:
   ```bash
   sudo zypper in rrdtool-devel
   ```

   On macOS:

   ```bash
   brew install rrdtool pkg-config
   ```

2. Build from source:

   ```bash
   git clone https://github.com/doublemarket/grafana-rrd-server.git
   cd grafana-rrd-server
   go build -o grafana-rrd-server .
   ```

   Or download [the latest release](https://github.com/doublemarket/grafana-rrd-server/releases/latest), gunzip it, and put the file in a directory included in `$PATH`:

   ```bash
   gunzip grafana-rrd-server_linux_amd64.gz
   chmod +x grafana-rrd-server_linux_amd64
   sudo mv grafana-rrd-server_linux_amd64 /usr/local/bin/grafana-rrd-server
   ```

3. Run the server.

   ```bash
   grafana-rrd-server
   ```

   You can use the following options:

   - `-h` : Shows help messages.
   - `-p` : Specifies server port. (default: 9000)
   - `-i` : Specifies server listen address. (default: any)
   - `-r` : Specifies a directory path keeping RRD files. (default: "./sample/")
     - The server recursively searches RRD files under the directory and returns a list of them for the `/search` endpoint.
   - `-a` : Specifies the annotations file. It should be a CSV file which has a title line at the top like [the sample file](https://github.com/doublemarket/grafana-rrd-server/tree/master/sample/annotations.csv).
   - `-s` : Default graph step in second. (default: 10)
     - You can see the step for your RRD file using:
       ```bash
       rrdtool info [rrd file] | grep step
       ```
   - `-c` : Search cache refresh interval in seconds. (default: 600)
   - `-m` : Value multiplier. (default: 1)
   - `-d` : RRDCached daemon address for network-based or remote RRD access (optional)
     - Examples: `unix:/var/run/rrdcached.sock` or `localhost:42217`
     - Enables full rrdcached support for both read and write operations
     - Recommended for network access to RRD files and write-heavy workloads

4. Optionally set up systemd unit:

```bash
useradd -r -s /bin/false grafanarrd
cat > /etc/systemd/system/grafana-rrd-server.service <<EOF
[Unit]
Description=Grafana RRD Server
After=network.service

[Service]
User=grafanarrd
Group=grafanarrd
Restart=on-failure
Environment="LD_LIBRARY_PATH=/opt/rrdtool-1.6/lib"
ExecStart=/usr/local/bin/grafana-rrd-server -p 9000 -r /path/to/rrds -s 300
RestartSec=10s

[Install]
WantedBy=default.target
EOF

systemctl daemon-reload
systemctl enable grafana-rrd-server
systemctl start grafana-rrd-server
```

5. Setup Grafana and datasource plugin.

   **Recommended:** Install the [Infinity Datasource plugin](https://grafana.com/grafana/plugins/yesoreyeram-infinity-datasource/):
   
   - In Grafana, go to Configuration → Plugins
   - Search for "Infinity"
   - Click Install
   - See [Infinity JSON API documentation](https://grafana.com/docs/plugins/yesoreyeram-infinity-datasource/latest/json) for configuration

   **Legacy:** Alternatively, use the deprecated [Simple JSON Datasource plugin](https://grafana.net/plugins/grafana-simple-json-datasource)

   See [Grafana documentation](http://docs.grafana.org/) for more details.

6. Create datasource pointing to your grafana-rrd-server instance (default: `http://localhost:9000`).

## Using with RRDCached

RRDCached is a daemon that receives updates to existing RRD files and accumulates them in memory before writing to disk. It also provides network access to RRD data. This server supports both direct file access and rrdcached-based access.

### Benefits of RRDCached

- **Reduced disk I/O**: Batches multiple updates before writing to disk
- **Better scalability**: Handles more concurrent operations  
- **Network access**: Query RRD files over the network from remote systems
- **Reduced file locking**: Less contention on RRD files
- **Centralized access**: Multiple systems can access the same RRD files through the daemon

### How It Works

When you specify the `-d` flag, grafana-rrd-server uses a hybrid approach:

- **With `-d` flag**: Uses the `multiplay/go-rrd` library to communicate with rrdcached over network protocol
  - All read operations (Info, Fetch) go through rrdcached
  - Ideal for remote RRD files or network-based monitoring setups
  
- **Without `-d` flag**: Uses the `ziutek/rrd` library for direct file access
  - Traditional local file access via librrd
  - Best for local RRD files with direct filesystem access

### Setting up RRDCached

1. Install and start rrdcached:

   ```bash
   # On Ubuntu/Debian
   sudo apt install rrdcached
   sudo systemctl start rrdcached
   sudo systemctl enable rrdcached
   
   # On CentOS/RHEL
   sudo yum install rrdtool
   # Create a systemd service for rrdcached
   
   # On macOS
   brew install rrdtool
   # Start manually: rrdcached -l unix:/tmp/rrdcached.sock -p /tmp/rrdcached.pid
   ```

2. Run grafana-rrd-server with rrdcached:

   ```bash
   # Unix socket (local)
   grafana-rrd-server -d unix:/var/run/rrdcached.sock -r /path/to/rrds
   
   # TCP socket (can be remote)
   grafana-rrd-server -d localhost:42217 -r /path/to/rrds
   
   # Remote rrdcached server
   grafana-rrd-server -d 192.168.1.100:42217 -r /path/to/rrds
   ```

### Performance Considerations

- **Local files**: Direct file access (no `-d` flag) is faster for local RRD files
- **Network files**: Use `-d` flag when RRD files are managed by a remote rrdcached instance
- **Hybrid setups**: You can run multiple instances - some with `-d` for remote files, some without for local files

## Docker

Build and run using Docker:

```bash
docker build -t grafana-rrd-server .
docker run -p 9000:9000 -v /path/to/rrds:/rrds grafana-rrd-server -r /rrds
```

# Development

## Building

1. Install librrd-dev (rrdtool) as described in the Usage section.

2. Clone the repository:

   ```bash
   git clone https://github.com/doublemarket/grafana-rrd-server.git
   cd grafana-rrd-server
   ```

3. Build:

   ```bash
   make build
   # or
   go build -o grafana-rrd-server .
   ```

## Testing

Run tests:

```bash
make test
# or
go test -v -parallel=4 .
```

## Running Locally

```bash
make run
# or
go run rrdserver.go
```

# Contributing

1. Install librrd-dev (rrdtool).

   See the Usage section.

2. Clone the repository.

3. Make your changes on a separate branch.

4. Ensure tests pass: `make test`

5. Create a pull request.

# License

MIT