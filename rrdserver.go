package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/mattn/go-zglob"
	rrdcached "github.com/multiplay/go-rrd"
	"github.com/ziutek/rrd"
)

var config Config
var logger *slog.Logger
var rrdcachedClient *rrdcached.Client
var rrdcachedMutex sync.Mutex

func init() {
	// Initialize logger with a default handler for tests
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}
}

type QueryResponse struct {
	Target     string      `json:"target"`
	DataPoints [][]float64 `json:"datapoints"`
}

type LsResponse struct {
	Directories []string `json:"directories"`
	Files       []string `json:"files"`
}

type SearchRequest struct {
	Target string `json:"target"`
}

type QueryRequest struct {
	PanelId interface{} `json:"panelId"`
	Range   struct {
		From string `json:"from"`
		To   string `json:"to"`
		Raw  struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"raw"`
	} `json:"range"`
	RangeRaw struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rangeRaw"`
	Interval   string `json:"interval"`
	IntervalMs int64  `json:"intervalMs"`
	Targets    []struct {
		Target string `json:"target"`
		RefID  string `json:"refId"`
		Hide   bool   `json:"hide"`
		Type   string `json:"type"`
	} `json:"targets"`
	Format        string `json:"format"`
	MaxDataPoints int64  `json:"maxDataPoints"`
}

type AnnotationResponse struct {
	Annotation string `json:"annotation"`
	Time       int64  `json:"time"`
	Title      string `json:"title"`
	Tags       string `json:"tags"`
	Text       string `json:"text"`
}

type AnnotationCSV struct {
	Time  int64  `csv:"time"`
	Title string `csv:"title"`
	Tags  string `csv:"tags"`
	Text  string `csv:"text"`
}

type AnnotationRequest struct {
	Range struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
	RangeRaw struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rangeRaw"`
	Annotation struct {
		Name       string `json:"name"`
		Datasource string `json:"datasource"`
		IconColor  string `json:"iconColor"`
		Enable     bool   `json:"enable"`
		Query      string `json:"query"`
	} `json:"annotation"`
}

type Config struct {
	Server ServerConfig
}

type ServerConfig struct {
	RrdPath            string
	Step               int
	SearchCache        int64
	IpAddr             string
	Port               int
	AnnotationFilePath string
	Multiplier         int
	RrdCached          string
}

type ErrorResponse struct {
	Message string `json:"message"`
}

type SearchCache struct {
	m     sync.Mutex
	items []string
}

func NewSearchCache() *SearchCache {
	return &SearchCache{}
}

func (w *SearchCache) Get() []string {
	w.m.Lock()
	defer w.m.Unlock()

	return w.items
}

func (w *SearchCache) Update() {
	newItems := []string{}

	logger.Info("Updating search cache")
	err := filepath.Walk(strings.TrimRight(config.Server.RrdPath, "/")+"/",
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || !strings.Contains(info.Name(), ".rrd") {
				return nil
			}
			rel, _ := filepath.Rel(config.Server.RrdPath, path)
			fName := strings.Replace(rel, ".rrd", "", 1)
			fName = strings.Replace(fName, "/", ":", -1)

			// Use rrdcached if configured, otherwise direct file access
			var dsIndex map[string]interface{}

			if rrdcachedClient != nil {
				// Use rrdcached client
				infoRes, err := rrdcachedClient.Info(path)
				if err != nil {
					logger.Error("Cannot retrieve information from RRD via rrdcached", "path", path, "error", err)
					return nil
				}
				// Parse ds.index from rrdcached Info response
				dsIndex = make(map[string]interface{})
				for _, info := range infoRes {
					if strings.HasPrefix(info.Key, "ds[") && strings.HasSuffix(info.Key, "].index") {
						dsName := strings.TrimSuffix(strings.TrimPrefix(info.Key, "ds["), "].index")
						dsIndex[dsName] = info.Value
					}
				}
			} else {
				// Use direct file access
				infoRes, err := rrd.Info(path)
				if err != nil {
					logger.Error("Cannot retrieve information from RRD file", "path", path, "error", err)
					return nil
				}
				dsIndex = infoRes["ds.index"].(map[string]interface{})
			}

			for ds := range dsIndex {
				newItems = append(newItems, fName+":"+ds)
			}

			return nil
		})

	if err != nil {
		logger.Error("Error walking path", "error", err)
		return
	}

	w.m.Lock()
	defer w.m.Unlock()
	w.items = newItems
	logger.Info("Finished updating search cache", "items", len(newItems))
}

var searchCache *SearchCache = NewSearchCache()

// recreateRRDCachedClient recreates the rrdcached client connection
func recreateRRDCachedClient() error {
	rrdcachedMutex.Lock()
	defer rrdcachedMutex.Unlock()

	if rrdcachedClient != nil {
		rrdcachedClient.Close()
		rrdcachedClient = nil
	}

	var err error
	if strings.HasPrefix(config.Server.RrdCached, "unix:") {
		socketPath := strings.TrimPrefix(config.Server.RrdCached, "unix:")
		rrdcachedClient, err = rrdcached.NewClient(socketPath, rrdcached.Unix)
	} else {
		rrdcachedClient, err = rrdcached.NewClient(config.Server.RrdCached)
	}

	if err != nil {
		logger.Error("Failed to reconnect to rrdcached", "daemon", config.Server.RrdCached, "error", err)
		return err
	}

	logger.Info("Reconnected to rrdcached", "daemon", config.Server.RrdCached)
	return nil
}

// fetchRRDData fetches data from RRD file, using rrdcached if configured
func fetchRRDData(filePath, cf string, start, end time.Time, step time.Duration) ([][]float64, []string, time.Time, time.Duration, int, error) {
	if rrdcachedClient != nil {
		// Use rrdcached client with retry logic
		startUnix := start.Unix()
		endUnix := end.Unix()

		// Flush the file first to ensure we get latest data
		// This is important when WRITE_TIMEOUT is high
		flushErr := rrdcachedClient.Flush(filePath)
		if flushErr != nil {
			logger.Warn("Failed to flush RRD file before fetch", "path", filePath, "error", flushErr)
		}

		var err error
		var fetch *rrdcached.Fetch

		// Retry up to 3 times with exponential backoff
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt*attempt) * time.Second
				logger.Warn("Retrying rrdcached fetch", "attempt", attempt+1, "backoff", backoff, "path", filePath)
				time.Sleep(backoff)
			}

			fetch, err = rrdcachedClient.Fetch(filePath, cf, startUnix, endUnix)
			if err == nil {
				break
			}

			logger.Error("RRDCached fetch failed", "path", filePath, "attempt", attempt+1, "error", err)

			// If timeout or connection error, try recreating the connection
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "connection") {
				logger.Warn("Recreating rrdcached connection due to timeout")
				if recreateErr := recreateRRDCachedClient(); recreateErr == nil {
					// Successfully reconnected, continue to next retry
					continue
				}
			}
		}

		if err != nil {
			return nil, nil, time.Time{}, 0, 0, err
		}

		// Convert multiplay fetch result to our format
		points := make([][]float64, len(fetch.Rows))
		for i, row := range fetch.Rows {
			points[i] = make([]float64, len(row.Data))
			for j, val := range row.Data {
				if val != nil {
					points[i][j] = *val
				} else {
					points[i][j] = math.NaN()
				}
			}
		}

		return points, fetch.Names, fetch.Start, fetch.Step, len(fetch.Rows), nil
	} else {
		// Use direct file access with ziutek/rrd
		fetchRes, err := rrd.Fetch(filePath, cf, start, end, step)
		if err != nil {
			return nil, nil, time.Time{}, 0, 0, err
		}

		// Convert ziutek result to our format
		dsCount := len(fetchRes.DsNames)
		points := make([][]float64, fetchRes.RowCnt)
		for i := 0; i < fetchRes.RowCnt; i++ {
			points[i] = make([]float64, dsCount)
			for j := 0; j < dsCount; j++ {
				points[i][j] = fetchRes.ValueAt(j, i)
			}
		}
		defer fetchRes.FreeValues()

		return points, fetchRes.DsNames, fetchRes.Start, fetchRes.Step, fetchRes.RowCnt, nil
	}
}

func respondJSON(w http.ResponseWriter, result interface{}) {
	json, err := json.Marshal(result)
	if err != nil {
		logger.Error("Cannot convert response data into JSON", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "accept, content-type")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,HEAD,OPTIONS")
	w.Write([]byte(json))
}

func hello(w http.ResponseWriter, r *http.Request) {
	result := ErrorResponse{Message: "hello"}
	respondJSON(w, result)
}

func ls(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "accept, content-type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,HEAD,OPTIONS")
		w.Write(nil)
		return
	}

	var searchRequest SearchRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&searchRequest)
		if err != nil && err.Error() != "EOF" {
			logger.Error("Cannot decode ls request", "error", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
	}

	target := searchRequest.Target
	prefix := target
	if prefix != "" {
		prefix = prefix + ":"
	}

	// Track directories and files at this level
	dirSet := make(map[string]bool)
	fileSet := make(map[string]bool)

	for _, path := range searchCache.Get() {
		// Remove datasource (everything after last colon)
		lastColon := strings.LastIndex(path, ":")
		if lastColon <= 0 {
			continue
		}
		filePath := path[:lastColon]

		// Check if this path is under our target directory
		if !strings.HasPrefix(filePath, prefix) {
			continue
		}

		// Get the relative path from the target
		relPath := strings.TrimPrefix(filePath, prefix)

		// If there's a colon, it's a subdirectory
		if strings.Contains(relPath, ":") {
			// Extract first directory component
			parts := strings.SplitN(relPath, ":", 2)
			dirSet[parts[0]] = true
		} else if relPath != "" {
			// It's a file at this level
			fileSet[relPath] = true
		}
	}

	// Convert to slices
	directories := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		directories = append(directories, dir)
	}

	files := make([]string, 0, len(fileSet))
	for file := range fileSet {
		files = append(files, file)
	}

	result := LsResponse{
		Directories: directories,
		Files:       files,
	}

	respondJSON(w, result)
}

func search(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var searchRequest SearchRequest
	err := decoder.Decode(&searchRequest)
	if err != nil {
		logger.Error("Cannot decode search request", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	target := searchRequest.Target

	var result = []string{}

	if target != "" {
		for _, path := range searchCache.Get() {
			if strings.Contains(path, target) {
				result = append(result, path)
			}
		}
	}

	respondJSON(w, result)
}

func query(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "accept, content-type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,HEAD,OPTIONS")
		w.Write(nil)
		return
	}
	decoder := json.NewDecoder(r.Body)
	var queryRequest QueryRequest
	err := decoder.Decode(&queryRequest)
	if err != nil {
		logger.Error("Cannot decode query request", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	from, _ := time.Parse(time.RFC3339Nano, queryRequest.Range.From)
	to, _ := time.Parse(time.RFC3339Nano, queryRequest.Range.To)

	var result []QueryResponse
	for _, target := range queryRequest.Targets {
		ds := target.Target[strings.LastIndex(target.Target, ":")+1 : len(target.Target)]
		rrdDsRep := regexp.MustCompile(`:` + ds + `$`)
		fileSearchPath := rrdDsRep.ReplaceAllString(target.Target, "")
		fileSearchPath = strings.TrimRight(config.Server.RrdPath, "/") + "/" + strings.Replace(fileSearchPath, ":", "/", -1) + ".rrd"

		fileNameArray, _ := zglob.Glob(fileSearchPath)
		for _, filePath := range fileNameArray {
			points := make([][]float64, 0)
			if _, err = os.Stat(filePath); err != nil {
				logger.Warn("File does not exist", "path", filePath)
				continue
			}

			// Get info using appropriate method
			var lastUpdate time.Time
			var dsIndex int

			if rrdcachedClient != nil {
				// Use rrdcached client for Info
				infoRes, err := rrdcachedClient.Info(filePath)
				if err != nil {
					logger.Error("Cannot retrieve information from RRD via rrdcached", "path", filePath, "error", err)
					continue
				}
				// Parse last_update and ds.index from rrdcached Info response
				for _, info := range infoRes {
					if info.Key == "last_update" {
						if val, ok := info.Value.(int64); ok {
							lastUpdate = time.Unix(val, 0)
						}
					}
					if strings.HasPrefix(info.Key, "ds["+ds+"].index") {
						if val, ok := info.Value.(int64); ok {
							dsIndex = int(val)
						}
					}
				}
			} else {
				// Use direct file access for Info
				infoRes, err := rrd.Info(filePath)
				if err != nil {
					logger.Error("Cannot retrieve information from RRD file", "path", filePath, "error", err)
					continue
				}
				lastUpdate = time.Unix(int64(infoRes["last_update"].(uint)), 0)
				dsIndex = int(infoRes["ds.index"].(map[string]interface{})[ds].(uint))
			}

			if to.After(lastUpdate) && lastUpdate.After(from) {
				to = lastUpdate
			}

			fetchData, dsNames, fetchStart, fetchStep, rowCnt, err := fetchRRDData(filePath, "AVERAGE", from, to, time.Duration(config.Server.Step)*time.Second)
			if err != nil {
				logger.Error("Cannot retrieve time series data from RRD file", "path", filePath, "error", err)
				continue
			}

			// Find dsIndex if we don't have it yet (rrdcached path)
			if rrdcachedClient != nil && dsIndex == 0 {
				for i, name := range dsNames {
					if name == ds {
						dsIndex = i
						break
					}
				}
			}

			timestamp := fetchStart
			// The last point is likely to contain wrong data (mostly a big number)
			// rowCnt-1 is for ignoring the last point (temporary solution)
			for i := 0; i < rowCnt-1; i++ {
				if dsIndex < len(fetchData[i]) {
					value := fetchData[i][dsIndex]
					if !math.IsNaN(value) {
						product := float64(config.Server.Multiplier) * value
						points = append(points, []float64{product, float64(timestamp.Unix()) * 1000})
					}
				}
				timestamp = timestamp.Add(fetchStep)
			}

			extractedTarget := strings.Replace(filePath, ".rrd", "", -1)
			extractedTarget = strings.Replace(extractedTarget, config.Server.RrdPath, "", -1)
			extractedTarget = strings.Replace(extractedTarget, "/", ":", -1) + ":" + ds
			result = append(result, QueryResponse{Target: extractedTarget, DataPoints: points})
		}
	}
	respondJSON(w, result)
}

func annotations(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "accept, content-type")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Write(nil)
		return
	}

	if config.Server.AnnotationFilePath == "" {
		result := ErrorResponse{Message: "Not configured"}
		respondJSON(w, result)
	} else {
		decoder := json.NewDecoder(r.Body)
		var annotationRequest AnnotationRequest
		err := decoder.Decode(&annotationRequest)
		defer r.Body.Close()
		if err != nil {
			logger.Error("Cannot decode annotation request", "error", err)
			result := ErrorResponse{Message: "Cannot decode the request"}
			respondJSON(w, result)
		} else {
			csvFile, err := os.OpenFile(config.Server.AnnotationFilePath, os.O_RDONLY, os.ModePerm)
			if err != nil {
				logger.Error("Cannot open annotations CSV file", "path", config.Server.AnnotationFilePath, "error", err)
			}
			defer csvFile.Close()
			annots := []*AnnotationCSV{}

			if err := gocsv.UnmarshalFile(csvFile, &annots); err != nil {
				logger.Error("Cannot unmarshal annotations CSV file", "error", err)
			}

			result := []AnnotationResponse{}
			from, _ := time.Parse(time.RFC3339Nano, annotationRequest.Range.From)
			to, _ := time.Parse(time.RFC3339Nano, annotationRequest.Range.To)
			for _, a := range annots {
				if (from.Unix()*1000) <= a.Time && a.Time <= (to.Unix()*1000) {
					result = append(result, AnnotationResponse{Annotation: "annotation", Time: a.Time, Title: a.Title, Tags: a.Tags, Text: a.Text})
				}
			}
			respondJSON(w, result)
		}
	}
}

func SetArgs() {
	flag.StringVar(&config.Server.IpAddr, "i", "", "Network interface IP address to listen on. (default: any)")
	flag.IntVar(&config.Server.Port, "p", 9000, "Server port.")
	flag.StringVar(&config.Server.RrdPath, "r", "./sample/", "Path for a directory that keeps RRD files.")
	flag.IntVar(&config.Server.Step, "s", 10, "Step in second.")
	flag.Int64Var(&config.Server.SearchCache, "c", 600, "Search cache in seconds.")
	flag.StringVar(&config.Server.AnnotationFilePath, "a", "", "Path for a file that has annotations.")
	flag.IntVar(&config.Server.Multiplier, "m", 1, "Value multiplier.")
	flag.StringVar(&config.Server.RrdCached, "d", "", "RRDCached daemon address (e.g., unix:/var/run/rrdcached.sock or localhost:42217).")
	flag.Parse()
}

func main() {
	SetArgs()

	// Initialize structured logger
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	logAttrs := []any{
		"port", config.Server.Port,
		"rrdPath", config.Server.RrdPath,
		"step", config.Server.Step,
	}

	// Initialize rrdcached client if configured
	if config.Server.RrdCached != "" {
		var err error
		// Determine if Unix socket or TCP
		if strings.HasPrefix(config.Server.RrdCached, "unix:") {
			socketPath := strings.TrimPrefix(config.Server.RrdCached, "unix:")
			rrdcachedClient, err = rrdcached.NewClient(socketPath, rrdcached.Unix)
		} else {
			rrdcachedClient, err = rrdcached.NewClient(config.Server.RrdCached)
		}
		if err != nil {
			logger.Error("Failed to connect to rrdcached, falling back to direct file access",
				"daemon", config.Server.RrdCached, "error", err)
			rrdcachedClient = nil
		} else {
			logAttrs = append(logAttrs, "rrdcached", config.Server.RrdCached)
			logger.Info("Connected to rrdcached successfully", "daemon", config.Server.RrdCached)
			defer rrdcachedClient.Close()
		}
	}

	logger.Info("Starting Grafana RRD Server", logAttrs...)

	http.HandleFunc("/ls", ls)
	http.HandleFunc("/search", search)
	http.HandleFunc("/query", query)
	http.HandleFunc("/annotations", annotations)
	http.HandleFunc("/", hello)

	// Start search cache updater
	go func() {
		for {
			searchCache.Update()
			time.Sleep(time.Duration(config.Server.SearchCache) * time.Second)
		}
	}()

	// Create HTTP server with timeouts
	// Longer timeouts to accommodate slow rrdcached responses
	server := &http.Server{
		Addr:         config.Server.IpAddr + ":" + strconv.Itoa(config.Server.Port),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		logger.Info("Server listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server error", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// Give outstanding requests 30 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err)
		os.Exit(1)
	}

	logger.Info("Server exited gracefully")
}
