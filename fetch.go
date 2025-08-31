package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// ---- Status enums (numeric parity with Python) ----
const (
	TaskSuccess       = 0
	TaskFatalError    = 1
	TaskWarning       = 2
	TaskPartialError  = 3
	TaskHandledError  = 4
	TaskNotApplicable = 5
	TaskInProgress    = 6
	TaskUnknown       = 7

	ResourceUp      = 0
	ResourceDown    = 1
	ResourceUnknown = 2
)

var taskStatusMap = map[string]int{
	"success":        TaskSuccess,
	"fatal_error":    TaskFatalError,
	"warning":        TaskWarning,
	"partial_error":  TaskPartialError,
	"handled_error":  TaskHandledError,
	"not_applicable": TaskNotApplicable,
	"in_progress":    TaskInProgress,
	"unknown":        TaskUnknown,
}
var resourceStatusMap = map[string]int{
	"up":      ResourceUp,
	"down":    ResourceDown,
	"unknown": ResourceUnknown,
}

// ---- HTTP / JSON ----
func fetchJSON(cli *retryablehttp.Client, url, user, pass string) (map[string]any, error) {
	req, err := retryablehttp.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if user != "" && pass != "" {
		req.SetBasicAuth(user, pass)
	}

	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("HTTP %d from %s body=%q", resp.StatusCode, url, string(body))
	}

	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid JSON from %s: %w", url, err)
	}
	return out, nil
}

func safeGet(m map[string]any, path ...string) (any, bool) {
	cur := any(m)
	for _, k := range path {
		asMap, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := asMap[k]
		if !ok || v == nil {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func getString(m map[string]any, key string, def string) string {
	if v, ok := m[key]; ok && v != nil {
		switch s := v.(type) {
		case string:
			return s
		default:
			return fmt.Sprintf("%v", s)
		}
	}
	return def
}

func extractFirst(m map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

func parseTimestamp(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		if f > 1e12 {
			return f / 1000.0, true
		}
		return f, true
	case float64:
		if t > 1e12 {
			return t / 1000.0, true
		}
		return t, true
	case int64:
		if t > 1e12 {
			return float64(t) / 1000.0, true
		}
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if isDigits(s) {
			f, err := parseFloat(s)
			if err == nil {
				if f > 1e12 {
					return f / 1000.0, true
				}
				return f, true
			}
		}
		// ISO8601 common layouts
		layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"}
		for _, l := range layouts {
			if ts, err := time.Parse(l, s); err == nil {
				return float64(ts.Unix()), true
			}
			if strings.HasSuffix(s, "Z") {
				s2 := strings.TrimSuffix(s, "Z") + "+00:00"
				if ts, err := time.Parse(time.RFC3339, s2); err == nil {
					return float64(ts.Unix()), true
				}
			}
		}
	}
	return 0, false
}

// ---- Exporters ----
func exportTaskMetrics(m *Metrics, data map[string]any) {
	v, ok := safeGet(data, "object", "object")
	if !ok {
		logWarn("Unexpected tasks payload structure — skipping")
		return
	}
	list, ok := v.([]any)
	if !ok {
		logWarn("Unexpected tasks payload list — skipping")
		return
	}

	for _, it := range list {
		task, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := getString(task, "name", "unknown_task")
		oid := getString(task, "oid", "unknown_oid")
		rs := strings.ToLower(getString(task, "resultStatus", "unknown"))
		status, ok := taskStatusMap[rs]
		if !ok {
			status = TaskUnknown
		}
		m.taskStatus.WithLabelValues(name, oid, m.hostLabel, m.jobTasks, m.appLabel).Set(float64(status))

		var startRaw, finishRaw any
		if v, ok := extractFirst(task, "lastRunStartTimestamp", "lastRunStartedTimestamp"); ok {
			startRaw = v
		}
		if v, ok := extractFirst(task, "lastRunFinishTimestamp", "lastRunStoppedTimestamp"); ok {
			finishRaw = v
		}
		startTS, hasStart := parseTimestamp(startRaw)
		finishTS, hasFinish := parseTimestamp(finishRaw)
		var duration float64
		var set bool
		if hasStart && hasFinish && finishTS >= startTS {
			duration = finishTS - startTS
			set = true
		} else if hasStart && status == TaskInProgress {
			now := float64(time.Now().Unix())
			duration = math.Max(0, now-startTS)
			set = true
		}
		if set {
			m.taskDuration.WithLabelValues(name, oid, m.hostLabel, m.jobTasks, m.appLabel).Set(duration)
		}
	}
}

func exportResourceMetrics(m *Metrics, data map[string]any) {
	v, ok := safeGet(data, "object", "object")
	if !ok {
		logWarn("Unexpected resources payload structure — skipping")
		return
	}
	list, ok := v.([]any)
	if !ok {
		logWarn("Unexpected resources payload list — skipping")
		return
	}
	for _, it := range list {
		res, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := getString(res, "name", "unknown_resource")
		oid := getString(res, "oid", "unknown_oid")
		statusRaw := "unknown"
		if os1, ok := res["operationalState"].(map[string]any); ok {
			statusRaw = strings.ToLower(getString(os1, "lastAvailabilityStatus", "unknown"))
		}
		status, ok := resourceStatusMap[statusRaw]
		if !ok {
			status = ResourceUnknown
		}
		m.resourceStatus.WithLabelValues(name, oid, m.hostLabel, m.jobResources, m.appLabel).Set(float64(status))
	}
}
