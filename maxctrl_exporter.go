// Copyright 2019, Vitaly Bezgachev, vitaly.bezgachev [the_at_symbol] gmail.com, Kadir Tugan, kadir.tugan [the_at_symbol] gmail.com
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	metricsPath = "/metrics"
	localIP     = "0.0.0.0"
)

// MaxScale contains connection parameters to the server and metric maps
type MaxScale struct {
	address               string
	username              string
	password              string
	up                    prometheus.Gauge
	totalScrapes          prometheus.Counter
	serverMetrics         map[string]Metric
	serviceMetrics        map[string]Metric
	maxscaleStatusMetrics map[string]Metric
	statusMetrics         map[string]Metric
}

// NewExporter creates a new instance of the MaxScale
func NewExporter(address string, username string, password string) (*MaxScale, error) {
	return &MaxScale{
		address:  address,
		username: username,
		password: password,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "up",
			Help:      "Was the last scrape of MaxScale successful?",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "exporter_total_scrapes",
			Help:      "Current total MaxScale scrapes",
		}),
		serverMetrics:         ServerMetrics,
		serviceMetrics:        ServiceMetrics,
		maxscaleStatusMetrics: MaxscaleStatusMetrics,
		statusMetrics:         StatusMetrics,
	}, nil
}

// Describe describes all the metrics ever exported by the MaxScale exporter. It
// implements prometheus.Collector.
func (m *MaxScale) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range m.serverMetrics {
		ch <- m.Desc
	}

	for _, m := range m.serviceMetrics {
		ch <- m.Desc
	}

	for _, m := range m.maxscaleStatusMetrics {
		ch <- m.Desc
	}

	for _, m := range m.statusMetrics {
		ch <- m.Desc
	}

	ch <- m.up.Desc()
	ch <- m.totalScrapes.Desc()
}

// Collect fetches the stats from configured MaxScale location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (m *MaxScale) Collect(ch chan<- prometheus.Metric) {
	m.totalScrapes.Inc()

	var parseErrors = false

	if err := m.parseServers(ch); err != nil {
		parseErrors = true
		log.Print(err)
	}

	if err := m.parseServices(ch); err != nil {
		parseErrors = true
		log.Print(err)
	}

	if err := m.parseMaxscaleStatus(ch); err != nil {
		parseErrors = true
		log.Print(err)
	}

	if err := m.parseThreadStatus(ch); err != nil {
		parseErrors = true
		log.Print(err)
	}

	if parseErrors {
		m.up.Set(0)
	} else {
		m.up.Set(1)
	}

	ch <- m.up
	ch <- m.totalScrapes
}

func (m *MaxScale) getStatistics(path string, v interface{}) error {
	var err error
	req, err := http.NewRequest("GET", "http://"+m.address+"/v1"+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(m.username, m.password)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error while getting %v: %v", path, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		err = fmt.Errorf("the MaxScale statistic request failed with a status: %s", resp.Status)
		return err
	}

	return json.NewDecoder(resp.Body).Decode(v)
}

func serverUp(status string) int {
	if strings.Contains(status, ",Down,") {
		return 0
	}
	if strings.Contains(status, ",Running,") {
		return 1
	}
	return 0
}

func (m *MaxScale) createMetricForPrometheus(metricsMap map[string]Metric, metricKey string,
	value int, ch chan<- prometheus.Metric, labelValues ...string) {

	metric := metricsMap[metricKey]
	ch <- prometheus.MustNewConstMetric(
		metric.Desc,
		metric.ValueType,
		float64(value),
		labelValues...,
	)
}

func (m *MaxScale) parseServers(ch chan<- prometheus.Metric) error {
	var servers Servers
	err := m.getStatistics("/servers", &servers)

	if err != nil {
		return err
	}

	for _, server := range servers.Data {
		serverID := server.ID
		serverAddress := server.Attributes.Parameters.Address
		m.createMetricForPrometheus(m.serverMetrics, "server_connections",
			server.Attributes.Statistics.Connections, ch, serverID, serverAddress)

		// We surround the separated list with the separator as well. This way regular expressions
		// in labeling don't have to consider satus positions.
		normalizedStatus := "," + strings.Replace(server.Attributes.State, ", ", ",", -1) + ","
		m.createMetricForPrometheus(m.serverMetrics, "server_up",
			serverUp(normalizedStatus), ch, serverID, serverAddress, normalizedStatus)
	}

	return nil
}

func (m *MaxScale) parseServices(ch chan<- prometheus.Metric) error {
	var services Services
	err := m.getStatistics("/services", &services)

	if err != nil {
		return err
	}

	for _, service := range services.Data {
		m.createMetricForPrometheus(m.serviceMetrics, "service_current_sessions",
			service.Attributes.Connections, ch, service.ID, service.Attributes.Router)

		m.createMetricForPrometheus(m.serviceMetrics, "service_sessions_total",
			service.Attributes.Connections, ch, service.ID, service.Attributes.Router)
	}

	return nil
}

func (m *MaxScale) parseMaxscaleStatus(ch chan<- prometheus.Metric) error {
	var maxscaleStatus MaxscaleStatus
	err := m.getStatistics("/maxscale", &maxscaleStatus)

	if err != nil {
		return err
	}

	m.createMetricForPrometheus(m.maxscaleStatusMetrics, "status_uptime",
		maxscaleStatus.Data.Attributes.Uptime, ch)

	m.createMetricForPrometheus(m.maxscaleStatusMetrics, "status_threads",
		maxscaleStatus.Data.Attributes.Parameters.Threads, ch)

	return nil
}
func (m *MaxScale) parseThreadStatus(ch chan<- prometheus.Metric) error {
	var threadStatus ThreadStatus
	err := m.getStatistics("/maxscale/threads", &threadStatus)

	if err != nil {
		return err
	}

	for _, threadStatus := range threadStatus.Data {
		m.createMetricForPrometheus(m.statusMetrics, "status_read_events",
			threadStatus.Attributes.Stats.Reads, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_write_events",
			threadStatus.Attributes.Stats.Writes, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_error_events",
			threadStatus.Attributes.Stats.Errors, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_hangup_events",
			threadStatus.Attributes.Stats.Hangups, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_accept_events",
			threadStatus.Attributes.Stats.Accepts, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_avg_event_queue_length",
			threadStatus.Attributes.Stats.AvgEventQueueLength, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_max_event_queue_length",
			threadStatus.Attributes.Stats.MaxEventQueueLength, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_max_event_exec_time",
			threadStatus.Attributes.Stats.MaxExecTime, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_max_event_queue_time",
			threadStatus.Attributes.Stats.MaxQueueTime, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_current_descriptors",
			threadStatus.Attributes.Stats.CurrentDescriptors, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_total_descriptors",
			threadStatus.Attributes.Stats.TotalDescriptors, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_load_last_second",
			threadStatus.Attributes.Stats.Load.LastSecond, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_load_last_minute",
			threadStatus.Attributes.Stats.Load.LastMinute, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_load_last_hour",
			threadStatus.Attributes.Stats.Load.LastHour, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_query_classifier_cache_size",
			threadStatus.Attributes.Stats.QueryClassifierCache.Size, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_query_classifier_cache_inserts",
			threadStatus.Attributes.Stats.QueryClassifierCache.Inserts, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_query_classifier_cache_hits",
			threadStatus.Attributes.Stats.QueryClassifierCache.Hits, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_query_classifier_cache_misses",
			threadStatus.Attributes.Stats.QueryClassifierCache.Misses, ch, threadStatus.ID)
		m.createMetricForPrometheus(m.statusMetrics, "status_query_classifier_cache_evictions",
			threadStatus.Attributes.Stats.QueryClassifierCache.Evictions, ch, threadStatus.ID)
	}

	return nil
}

func main() {
	maxScaleAddress := os.Getenv("MAXSCALE_ADDRESS")
	if len(maxScaleAddress) == 0 {
		maxScaleAddress = "127.0.0.1"
	}

	maxScalePort := os.Getenv("MAXSCALE_PORT")
	if len(maxScalePort) == 0 {
		maxScalePort = "8989"
	}

	maxScaleUsername := os.Getenv("MAXSCALE_USERNAME")
	if len(maxScaleUsername) == 0 {
		maxScaleUsername = "admin"
	}

	maxScalePassword := os.Getenv("MAXSCALE_PASSWORD")
	if len(maxScalePassword) == 0 {
		maxScalePassword = "mariadb"
	}

	maxScaleExporterPort := os.Getenv("MAXSCALE_EXPORTER_PORT")
	if len(maxScaleExporterPort) == 0 {
		maxScaleExporterPort = "8080"
	}

	maxScaleFullAddress := maxScaleAddress + ":" + maxScalePort
	log.Print("Starting MaxScale exporter")
	log.Printf("Scraping MaxScale JSON API at: %s", maxScaleFullAddress)

	exporter, err := NewExporter(maxScaleFullAddress, maxScaleUsername, maxScalePassword)
	if err != nil {
		log.Fatalf("Failed to start maxscale exporter: %v\n", err)
	}

	prometheus.MustRegister(exporter)
	http.Handle(metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>
			<head><title>MaxScale Exporter</title></head>
			<body>
			<h1>MaxScale Exporter</h1>
			<p><a href="` + metricsPath + `">Metrics</a></p>
			</body>
			</html>`))
	})
	log.Printf("Started MaxScale exporter, listening on port: %v", maxScaleExporterPort)
	log.Fatal(http.ListenAndServe(localIP+":"+maxScaleExporterPort, nil))
}
