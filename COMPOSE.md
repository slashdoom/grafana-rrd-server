# Docker Compose Quick Start Guide

This Docker Compose setup provides a complete RRD monitoring stack with:
- **RRDCached**: Caching daemon for improved RRD performance
- **Grafana RRD Server**: HTTP API server with hybrid rrdcached support
- **Grafana**: Visualization platform with Infinity datasource pre-installed

## Quick Start

1. **Start the stack:**
   ```bash
   docker-compose up -d
   ```

2. **Access Grafana:**
   - URL: http://localhost:3000
   - Username: `admin`
   - Password: `admin`

3. **Verify services:**
   ```bash
   docker-compose ps
   ```

4. **View logs:**
   ```bash
   # All services
   docker-compose logs -f
   
   # Specific service
   docker-compose logs -f grafana-rrd-server
   docker-compose logs -f rrdcached
   docker-compose logs -f grafana
   ```

## Architecture

```
┌─────────┐      HTTP       ┌──────────────────────────────┐
│ Grafana │ ◄────────────── │ RRD Server (hybrid mode)     │
│         │   (port 9000)   │ Endpoints:                   │
│         │                 │  • /ls (directory browse)    │
│         │                 │  • /search (metric search)   │
│         │                 │  • /query (time series data) │
│         │                 │  • /annotations (events)     │
└─────────┘                 └──────────────────────────────┘
                                     │
                            ┌────────┴─────────┐
                            │                  │
                       Unix Socket        Direct File
                       (rrdcached)         Access
                            │              (ziutek/rrd)
                            ▼                  │
                      ┌──────────┐             │
                      │RRDCached │◄────────────┘
                      │  Daemon  │
                      └──────────┘
                            │
                            ▼
                      ./sample/*.rrd
```

### How It Works

1. **Grafana** sends HTTP requests to the RRD Server (port 9000)
2. **RRD Server** uses hybrid mode:
   - **With rrdcached** (`-d` flag): Uses `multiplay/go-rrd` library via Unix socket
   - **Without rrdcached**: Uses `ziutek/rrd` library for direct file access
   - Both containers: `-c` (rrdcached) and `-f` (file-only) demonstrate both modes
3. **RRDCached** (optional) provides caching and network access to RRD files
4. **Data** is read from `./sample/` directory (mounted read-only)

## Service Details

### RRDCached
- **Purpose**: Caching daemon for RRD operations
- **Socket**: `/var/run/rrdcached.sock` (shared volume)
- **Data**: Reads from `./sample` directory (read-only)
- **Journal**: Persistent volume for write operations

### Grafana RRD Server (Two Instances)

#### grafana-rrd-server-c (with rrdcached)
- **Port**: Internal only (accessed via Docker network)
- **Mode**: Hybrid with rrdcached connection
- **Connection**: Unix socket at `/var/run/rrdcached.sock`
- **Flag**: `-d unix:/var/run/rrdcached.sock`
- **Purpose**: Demonstrates rrdcached-based access

#### grafana-rrd-server-f (file-only)
- **Port**: 9000 (mapped to host)
- **Mode**: Direct file access only
- **Connection**: No rrdcached
- **Purpose**: Demonstrates traditional local file access
- **Use Case**: Best for local RRD files with direct filesystem access

### Grafana
- **Port**: 3000 (mapped to host)
- **Plugin**: Infinity datasource (auto-installed)
- **Datasource**: Pre-configured to point to grafana-rrd-server
- **Data**: Persistent volume for dashboards and settings

## Using the Infinity Datasource

Two datasources are pre-configured:
- **grafana-rrd-server-c**: RRD server with rrdcached
- **grafana-rrd-server-f**: RRD server with direct file access

### Querying Time Series Data

1. **Create a new panel** in Grafana

2. **Select a datasource** (either grafana-rrd-server-c or grafana-rrd-server-f)

3. **Configure the query:**
   - **Type**: JSON
   - **Parser**: Backend
   - **Format**: Time series
   - **Method**: POST
   - **URL**: `/query`
   - **Root Selector**: `$[0].datapoints`
   - **Columns**:
     - Selector: `1`, Type: `Timestamp (ms)`, Name: `time`
     - Selector: `0`, Type: `Number`, Name: `value`
   - **Body**: 
     ```json
     {
       "range": {
         "from": "${__from:date:iso}",
         "to": "${__to:date:iso}"
       },
       "targets": [
         {
           "target": "percent-user:value",
           "refId": "A",
           "type": "timeseries"
         }
       ]
     }
     ```

### Browsing Available Files

Use the `/ls` endpoint to discover files:

```json
{
  "Type": "JSON",
  "Parser": "Backend",
  "Format": "Table",
  "Method": "POST",
  "URL": "/ls",
  "Body": {
    "target": ""
  }
}
```

Response shows directories and files at the current level.

### Searching for Metrics

Use the `/search` endpoint to find metrics:

```json
{
  "Type": "JSON",
  "Parser": "Backend",
  "Format": "Table",
  "Method": "POST",
  "URL": "/search",
  "Body": {
    "target": "percent-user"
  }
}
```

Returns all metrics matching the search term.

## Testing RRDCached Connection

Test that rrdcached is working:

```bash
# Enter the grafana-rrd-server container
docker-compose exec grafana-rrd-server sh

# Test the socket exists
ls -la /var/run/rrdcached.sock

# Check server logs for rrdcached connection
docker-compose logs grafana-rrd-server | grep rrdcached
```

You should see:
```
"msg":"Connected to rrdcached successfully","daemon":"unix:/var/run/rrdcached.sock"
```

## Testing Without RRDCached

To run the server in direct file access mode (no rrdcached):

```bash
# Edit docker-compose.yml and change the command:
command: ["-r", "/rrds", "-p", "9000"]

# Restart the service
docker-compose restart grafana-rrd-server
```

## Configuration

### Environment Variables

Create a `.env` file from `.env.example`:

```bash
cp .env.example .env
```

Edit `.env` to customize:
- `GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD`
- `GRAFANA_PORT` / `RRD_SERVER_PORT`
- `RRD_PATH`

### Custom RRD Files

Replace the `./sample` directory with your own RRD files:

```bash
# Edit docker-compose.yml
volumes:
  - /path/to/your/rrds:/var/lib/rrdcached/db/sample:ro
```

## Troubleshooting

### Grafana can't reach RRD server
```bash
# Check if grafana-rrd-server is running
docker-compose ps grafana-rrd-server

# Check logs
docker-compose logs grafana-rrd-server

# Test from Grafana container
docker-compose exec grafana wget -O- http://grafana-rrd-server:9000/
```

### RRDCached socket not found
```bash
# Check rrdcached is running
docker-compose ps rrdcached

# Verify socket exists
docker-compose exec rrdcached ls -la /var/run/rrdcached.sock

# Check shared volume
docker volume inspect grafana-rrd-server_rrdcached-socket
```

### Infinity plugin not installed
```bash
# Check installed plugins
docker-compose exec grafana grafana-cli plugins ls

# Reinstall if needed
docker-compose down
docker volume rm grafana-rrd-server_grafana-data
docker-compose up -d
```

## Stopping the Stack

```bash
# Stop services (keeps data)
docker-compose down

# Stop and remove all data
docker-compose down -v
```

## Performance Notes

- **With RRDCached**: Better for write-heavy workloads and network access
- **Direct File Access**: Faster for read-heavy workloads with local files
- **Hybrid Mode** (current setup): Uses rrdcached when available, falls back to direct access

The current setup demonstrates the hybrid mode, which is optimal for most use cases.

## Provisioned Dashboard

A sample dashboard is automatically provisioned on startup:

- **Name**: "Sample"
- **Location**: Dashboards → Sample
- **Features**:
  - `/ls` endpoint demonstration (file browsing)
  - `/search` endpoint demonstration (metric search)
  - Time series charts from both RRD server instances (rrdcached vs direct file)
  - Side-by-side comparison of both access methods

### Dashboard Structure

- **Top Left**: `/ls` endpoint results (directory/file listing)
- **Top Right**: Time series from grafana-rrd-server-c (with rrdcached)
- **Bottom Left**: `/search` endpoint results (metric search)
- **Bottom Right**: Time series from grafana-rrd-server-f (direct file access)

The dashboard uses the sample `percent-user.rrd` file data from December 7-8, 2016.

## Next Steps

1. Explore the provisioned "Sample" dashboard
2. Create custom dashboards using the `/ls` and `/search` endpoints
3. Compare performance between rrdcached and direct file access
4. Configure additional datasources for your own RRD files
5. Explore the Infinity datasource features
6. Add alerting rules

For more information, see the main [README.md](README.md).
