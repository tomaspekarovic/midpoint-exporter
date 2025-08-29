"""MidPoint Prometheus Exporter (refactored & expanded)

- Fetches task and resource status from MidPoint REST API
- Exposes them as Prometheus metrics on an HTTP endpoint

Environment variables:
- PORT (int, default 9993) — Prometheus metrics port
- SCRAPE_INTERVAL (int seconds, default 30)
- MIDPOINT_BASE_URL (required) — e.g. https://midpoint.example
- MIDPOINT_TASKS_URL (suffix, default "/ws/rest/tasks")
- MIDPOINT_RESOURCES_URL (suffix, default "/ws/rest/resources")
- MIDPOINT_USER (optional if mTLS-only)
- MIDPOINT_PASSWORD (optional if mTLS-only)
- APP_LABEL (default "mp") — metric label
- JOB_LABEL_PREFIX (default "midpoint") — "-task"/"-resource" will be appended
- MIDPOINT_VERIFY_TLS ("true"/"false", default "true") — ignored if CA_BUNDLE provided
- CA_BUNDLE (path to CA certs file/dir; overrides MIDPOINT_VERIFY_TLS when set)
- CLIENT_CERT (path to client cert PEM)
- CLIENT_KEY (path to client key PEM)
- CLIENT_CERT_COMBINED (path to combined client cert+key PEM; preferred if set)
- LOG_LEVEL (DEBUG, INFO, WARNING, ERROR; default INFO)
"""
from __future__ import annotations

import json
import logging
import os
import signal
import sys
import time
from dataclasses import dataclass
from enum import IntEnum
from typing import Any, Dict, Iterable, Mapping, Optional, Tuple
from urllib.parse import urljoin, urlparse

import requests
import urllib3
from prometheus_client import Gauge, start_http_server
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


# ----------------------------
# Enums
# ----------------------------
class TaskStatus(IntEnum):
    SUCCESS = 0
    FATAL_ERROR = 1
    WARNING = 2
    PARTIAL_ERROR = 3
    HANDLED_ERROR = 4
    NOT_APPLICABLE = 5
    IN_PROGRESS = 6
    UNKNOWN = 7


class ResourceStatus(IntEnum):
    UP = 0
    DOWN = 1
    UNKNOWN = 2


# ----------------------------
# Configuration
# ----------------------------
@dataclass(frozen=True)
class Config:
    port: int = int(os.getenv("PORT", "9993"))
    interval: int = int(os.getenv("SCRAPE_INTERVAL", "30"))
    base_url: str = os.getenv("MIDPOINT_BASE_URL", "")
    tasks_suffix: str = os.getenv("MIDPOINT_TASKS_URL", "/ws/rest/tasks")
    resources_suffix: str = os.getenv("MIDPOINT_RESOURCES_URL", "/ws/rest/resources")
    username: str = os.getenv("MIDPOINT_USER", "")
    password: str = os.getenv("MIDPOINT_PASSWORD", "")
    app_label: str = os.getenv("APP_LABEL", "mp")
    job_prefix: str = os.getenv("JOB_LABEL_PREFIX", "midpoint")
    verify_tls: bool = os.getenv("MIDPOINT_VERIFY_TLS", "true").lower() in {"1", "true", "yes"}
    ca_bundle: str = os.getenv("CA_BUNDLE", "")
    client_cert_combined: str = os.getenv("CLIENT_CERT_COMBINED", "")
    client_cert: str = os.getenv("CLIENT_CERT", "")
    client_key: str = os.getenv("CLIENT_KEY", "")
    log_level: str = os.getenv("LOG_LEVEL", "INFO")

    def validate(self) -> None:
        missing: list[str] = []
        if not self.base_url:
            missing.append("MIDPOINT_BASE_URL")
        if missing:
            raise ValueError(f"Missing required env vars: {', '.join(missing)}")

    @property
    def tasks_url(self) -> str:
        return compose_url(self.base_url, self.tasks_suffix)

    @property
    def resources_url(self) -> str:
        return compose_url(self.base_url, self.resources_suffix)


# ----------------------------
# Utilities
# ----------------------------

def compose_url(base: str, suffix: str) -> str:
    """Join base and suffix into a proper URL regardless of slashes.

    Important: if `base` contains a path (e.g., http://host:8080/midpoint) we must
    NOT start `suffix` with a leading '/', otherwise urljoin would reset to the root
    and drop the base path. So we strip leading slashes from the suffix.
    """
    if not base.endswith('/'):
        base = base + '/'
    # Remove leading '/' to preserve base path components
    clean_suffix = suffix.lstrip('/')
    return urljoin(base, clean_suffix)


# ----------------------------
# HTTP Session with retries & mTLS
# ----------------------------

def build_session(cfg: Config) -> requests.Session:
    if not cfg.ca_bundle and not cfg.verify_tls:
        urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

    session = requests.Session()
    retry = Retry(
        total=3,
        connect=3,
        read=3,
        backoff_factor=0.5,
        status_forcelist=(429, 500, 502, 503, 504),
        allowed_methods=("GET",),
        raise_on_status=False,
    )
    adapter = HTTPAdapter(max_retries=retry, pool_connections=10, pool_maxsize=10)
    session.mount("http://", adapter)
    session.mount("https://", adapter)

    # TLS verification: use CA_BUNDLE if set, else bool flag
    session.verify = cfg.ca_bundle if cfg.ca_bundle else cfg.verify_tls

    # mTLS client certificate
    cert: Optional[Tuple[str, str]] | Optional[str]
    cert = None
    if cfg.client_cert_combined:
        cert = cfg.client_cert_combined
    elif cfg.client_cert and cfg.client_key:
        cert = (cfg.client_cert, cfg.client_key)
    if cert:
        session.cert = cert

    session.headers.update({
        "Accept": "application/json",
        "Content-Type": "application/json",
    })
    return session


# ----------------------------
# Metrics
# ----------------------------
class Metrics:
    def __init__(self, base_url: str, app_label: str, job_prefix: str) -> None:
        # Derive a stable label that keeps host:port and base path (no scheme)
        parsed = urlparse(base_url)
        netloc = parsed.netloc or parsed.hostname or "unknown"
        path = parsed.path or ""
        self.host_label = f"{netloc}{path}".rstrip('/') or netloc or "unknown"
        self.task_status = Gauge(
            "midpoint_task_status",
            "MidPoint Task status",
            ["task_name", "oid", "host", "job", "app"],
        )
        self.task_duration = Gauge(
            "midpoint_task_duration_seconds",
            "MidPoint Task run duration in seconds",
            ["task_name", "oid", "host", "job", "app"],
        )
        self.resource_status = Gauge(
            "midpoint_resource_status",
            "MidPoint Resource status",
            ["resource_name", "oid", "host", "job", "app"],
        )
        self.scrape_success = Gauge(
            "midpoint_last_scrape_success",
            "Was the last scrape cycle successful (1) or had errors (0)",
            ["host", "job", "app"],
        )
        self.scrape_duration = Gauge(
            "midpoint_scrape_duration_seconds",
            "Duration of a full scrape cycle in seconds",
            ["host", "job", "app"],
        )
        self.app_label = app_label
        self.job_tasks = f"{job_prefix}-task"
        self.job_resources = f"{job_prefix}-resource"


# ----------------------------
# Fetching
# ----------------------------

def fetch_json(session: requests.Session, url: str, auth: Optional[tuple[str, str]] = None, timeout: int = 10) -> Dict[str, Any]:
    """Fetch JSON from URL and return parsed dict.
    Raises requests.HTTPError for 4xx/5xx responses.
    """
    resp = session.get(url, auth=auth, timeout=timeout)
    try:
        resp.raise_for_status()
    except requests.HTTPError:
        # Include response body (truncated) for easier debugging
        body = resp.text[:500]
        logging.error("HTTP error from %s: %s | body=%s", url, resp.status_code, body)
        raise
    try:
        return resp.json()  # type: ignore[return-value]
    except json.JSONDecodeError as e:
        logging.error("Invalid JSON from %s: %s", url, e)
        raise


# ----------------------------
# Processing
# ----------------------------

def _parse_timestamp(ts: Any) -> Optional[float]:
    """Return seconds since epoch for various timestamp formats.
    Accepts ISO8601 strings (with or without 'Z'), integer/float epoch seconds or ms.
    """
    if ts is None:
        return None
    try:
        if isinstance(ts, (int, float)):
            val = float(ts)
            # If looks like ms since epoch, convert
            return val / 1000.0 if val > 1e12 else val
        s = str(ts).strip()
        # numeric string
        if s.isdigit():
            val = float(s)
            return val / 1000.0 if val > 1e12 else val
        # ISO8601
        from datetime import datetime
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return datetime.fromisoformat(s).timestamp()
    except Exception:
        return None


def _extract_first(mapping: Mapping[str, Any], keys: Iterable[str]) -> Any:
    for k in keys:
        if k in mapping and mapping.get(k) is not None:
            return mapping.get(k)
    return None
TASK_STATUS_MAP: Mapping[str, TaskStatus] = {
    "success": TaskStatus.SUCCESS,
    "fatal_error": TaskStatus.FATAL_ERROR,
    "warning": TaskStatus.WARNING,
    "partial_error": TaskStatus.PARTIAL_ERROR,
    "handled_error": TaskStatus.HANDLED_ERROR,
    "not_applicable": TaskStatus.NOT_APPLICABLE,
    "in_progress": TaskStatus.IN_PROGRESS,
    "unknown": TaskStatus.UNKNOWN,
}

RESOURCE_STATUS_MAP: Mapping[str, ResourceStatus] = {
    "up": ResourceStatus.UP,
    "down": ResourceStatus.DOWN,
    "unknown": ResourceStatus.UNKNOWN,
}


def _safe_get(seq: Mapping[str, Any], path: Iterable[str], default: Any) -> Any:
    cur: Any = seq
    for key in path:
        if not isinstance(cur, Mapping):
            return default
        cur = cur.get(key)
        if cur is None:
            return default
    return cur


def export_task_metrics(metrics: Metrics, json_data: Mapping[str, Any]) -> None:
    """Read tasks from MidPoint JSON and export gauge values.

    Also exports midpoint_task_duration_seconds using the best available timestamps.
    It looks for (in order):
      - lastRunStartTimestamp / lastRunFinishTimestamp
      - lastRunStartedTimestamp / lastRunStoppedTimestamp
    If a task is currently in progress and only a start time is present, duration is
    computed as now - start.
    """
    tasks = _safe_get(json_data, ("object", "object"), [])
    if not isinstance(tasks, list):
        logging.warning("Unexpected tasks payload structure — skipping")
        return

    for task in tasks:
        if not isinstance(task, Mapping):
            continue
        name: str = str(task.get("name", "unknown_task"))
        oid: str = str(task.get("oid", "unknown_oid"))
        result_status_raw: str = str(task.get("resultStatus", "unknown")).lower()
        status = TASK_STATUS_MAP.get(result_status_raw, TaskStatus.UNKNOWN)
        metrics.task_status.labels(
            task_name=name,
            oid=oid,
            host=metrics.host_label,
            job=metrics.job_tasks,
            app=metrics.app_label,
        ).set(int(status))

        # Duration calculation
        start_raw = _extract_first(task, (
            "lastRunStartTimestamp",
            "lastRunStartedTimestamp",
        ))
        finish_raw = _extract_first(task, (
            "lastRunFinishTimestamp",
            "lastRunStoppedTimestamp",
        ))
        start_ts = _parse_timestamp(start_raw)
        end_ts = _parse_timestamp(finish_raw)
        duration: Optional[float] = None
        if start_ts is not None and end_ts is not None and end_ts >= start_ts:
            duration = end_ts - start_ts
        elif start_ts is not None and status == TaskStatus.IN_PROGRESS:
            duration = max(0.0, time.time() - start_ts)

        if duration is not None:
            metrics.task_duration.labels(
                task_name=name,
                oid=oid,
                host=metrics.host_label,
                job=metrics.job_tasks,
                app=metrics.app_label,
            ).set(duration)


def export_resource_metrics(metrics: Metrics, json_data: Mapping[str, Any]) -> None:
    """Read resources from MidPoint JSON and export gauge values."""
    resources = _safe_get(json_data, ("object", "object"), [])
    if not isinstance(resources, list):
        logging.warning("Unexpected resources payload structure — skipping")
        return

    for res in resources:
        if not isinstance(res, Mapping):
            continue
        name: str = str(res.get("name", "unknown_resource"))
        oid: str = str(res.get("oid", "unknown_oid"))
        result_status_raw: str = str(
            _safe_get(res, ("operationalState", "lastAvailabilityStatus"), "unknown")
        ).lower()
        status = RESOURCE_STATUS_MAP.get(result_status_raw, ResourceStatus.UNKNOWN)
        metrics.resource_status.labels(
            resource_name=name,
            oid=oid,
            host=metrics.host_label,
            job=metrics.job_resources,
            app=metrics.app_label,
        ).set(int(status))


# ----------------------------
# Main loop
# ----------------------------
STOP = False


def _signal_handler(signum: int, frame: Optional[Any]) -> None:
    global STOP
    logging.info("Received signal %s — shutting down gracefully", signum)
    STOP = True


def run() -> None:
    cfg = Config()
    logging.basicConfig(
        level=getattr(logging, cfg.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(message)s",
        stream=sys.stdout,
    )

    try:
        cfg.validate()
    except ValueError as e:
        logging.error("Configuration error: %s", e)
        sys.exit(2)

    metrics = Metrics(cfg.base_url, cfg.app_label, cfg.job_prefix)

    # Start Prometheus HTTP endpoint
    start_http_server(cfg.port)
    logging.info("Serving Prometheus metrics on :%d", cfg.port)

    # Build HTTP session (with optional mTLS)
    session = build_session(cfg)

    # Handle termination signals
    signal.signal(signal.SIGTERM, _signal_handler)
    signal.signal(signal.SIGINT, _signal_handler)

    auth: Optional[tuple[str, str]] = None
    if cfg.username and cfg.password:
        auth = (cfg.username, cfg.password)
    else:
        logging.info("No basic auth credentials configured — assuming mTLS or anonymous access")

    while not STOP:
        start_ts = time.time()
        success = 1
        try:
            tasks_json = fetch_json(session, cfg.tasks_url, auth)
            export_task_metrics(metrics, tasks_json)
        except Exception as e:
            success = 0
            logging.exception("Failed processing tasks: %s", e)

        try:
            resources_json = fetch_json(session, cfg.resources_url, auth)
            export_resource_metrics(metrics, resources_json)
        except Exception as e:
            success = 0
            logging.exception("Failed processing resources: %s", e)

        duration = time.time() - start_ts
        metrics.scrape_duration.labels(host=metrics.host_label, job="midpoint-scrape", app=cfg.app_label).set(duration)
        metrics.scrape_success.labels(host=metrics.host_label, job="midpoint-scrape", app=cfg.app_label).set(success)

        sleep_for = max(0.0, cfg.interval - duration)
        time.sleep(sleep_for)


if __name__ == "__main__":
    run()
