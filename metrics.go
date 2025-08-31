package main

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	hostLabel    string
	appLabel     string
	jobTasks     string
	jobResources string

	taskStatus     *prometheus.GaugeVec
	taskDuration   *prometheus.GaugeVec
	resourceStatus *prometheus.GaugeVec
	scrapeSuccess  *prometheus.GaugeVec
	scrapeDuration *prometheus.GaugeVec
}

func newMetrics(baseURL, appLabel, jobPrefix string) *Metrics {
	m := &Metrics{
		hostLabel:    hostLabelFromBaseURL(baseURL),
		appLabel:     appLabel,
		jobTasks:     jobPrefix + "-task",
		jobResources: jobPrefix + "-resource",
		taskStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "midpoint_task_status", Help: "MidPoint Task status"},
			[]string{"task_name", "oid", "host", "job", "app"},
		),
		taskDuration: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "midpoint_task_duration_seconds", Help: "MidPoint Task run duration in seconds"},
			[]string{"task_name", "oid", "host", "job", "app"},
		),
		resourceStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "midpoint_resource_status", Help: "MidPoint Resource status"},
			[]string{"resource_name", "oid", "host", "job", "app"},
		),
		scrapeSuccess: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "midpoint_last_scrape_success", Help: "Was the last scrape cycle successful (1) or had errors (0)"},
			[]string{"host", "job", "app"},
		),
		scrapeDuration: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "midpoint_scrape_duration_seconds", Help: "Duration of a full scrape cycle in seconds"},
			[]string{"host", "job", "app"},
		),
	}
	prometheus.MustRegister(m.taskStatus, m.taskDuration, m.resourceStatus, m.scrapeSuccess, m.scrapeDuration)
	return m
}
