package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	setLogLevel(cfg.LogLevel)

	metrics := newMetrics(cfg.BaseURL, cfg.AppLabel, cfg.JobPrefix)

	// Prometheus endpoint
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logInfo("Serving Prometheus metrics on :%d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("metrics server: %v", err)
		}
	}()

	client := buildHTTPClient()

	if cfg.Username == "" || cfg.Password == "" {
		logInfo("No basic auth credentials configured — assuming upstream (Nginx) handles auth")
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for {
		start := time.Now()
		success := 1.0

		// Tasks
		if tasksJSON, err := fetchJSON(client, cfg.TasksURL(), cfg.Username, cfg.Password); err != nil {
			logErr("Failed processing tasks: %v", err)
			success = 0
		} else {
			exportTaskMetrics(metrics, tasksJSON)
		}

		// Resources
		if resJSON, err := fetchJSON(client, cfg.ResourcesURL(), cfg.Username, cfg.Password); err != nil {
			logErr("Failed processing resources: %v", err)
			success = 0
		} else {
			exportResourceMetrics(metrics, resJSON)
		}

		dur := time.Since(start).Seconds()
		metrics.scrapeDuration.WithLabelValues(metrics.hostLabel, "midpoint-scrape", cfg.AppLabel).Set(dur)
		metrics.scrapeSuccess.WithLabelValues(metrics.hostLabel, "midpoint-scrape", cfg.AppLabel).Set(success)

		// Sleep respecting interval, but exit fast on shutdown
		sleepFor := cfg.Interval - time.Since(start)
		if sleepFor > 0 {
			select {
			case <-time.After(sleepFor):
			case <-ctx.Done():
				_ = srv.Shutdown(context.Background())
				return
			}
		}
		select {
		case <-ctx.Done():
			_ = srv.Shutdown(context.Background())
			return
		default:
		}
	}
}
