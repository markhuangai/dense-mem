package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

type cpuTimes struct{ total, idle, wait uint64 }

func parseCPU(data string) (cpuTimes, error) {
	fields := strings.Fields(strings.SplitN(data, "\n", 2)[0])
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("CPU counters unavailable")
	}
	var result cpuTimes
	// Guest counters are already included in user and nice.
	for index, field := range fields[1:9] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, err
		}
		result.total += value
		if index == 3 {
			result.idle = value
		}
		if index == 4 {
			result.wait = value
		}
	}
	return result, nil
}

func cpuInterval(previous, current cpuTimes) (float64, float64, bool) {
	if current.total <= previous.total || current.idle < previous.idle || current.wait < previous.wait {
		return 0, 0, false
	}
	total, idle, wait := current.total-previous.total, current.idle-previous.idle, current.wait-previous.wait
	if idle+wait > total {
		return 0, 0, false
	}
	return 100 * float64(total-idle-wait) / float64(total), 100 * float64(wait) / float64(total), true
}

type resourceSnapshot struct {
	Phase             string            `json:"phase"`
	IntervalMS        int64             `json:"interval_ms"`
	HostCPUPercent    *float64          `json:"host_cpu_percent"`
	HostIOWaitPercent *float64          `json:"host_io_wait_percent"`
	AvailableMemory   *uint64           `json:"host_available_memory_bytes"`
	DiskFree          *uint64           `json:"disk_free_bytes"`
	Containers        map[string]any    `json:"containers"`
	PGConnections     *int              `json:"postgres_connections"`
	PGActive          *int              `json:"postgres_active"`
	PGLockWaits       *int              `json:"postgres_lock_waits"`
	Unavailable       map[string]string `json:"unavailable"`
}

type resourceSampler struct {
	previous cpuTimes
	observed time.Time
}

func (s *resourceSampler) sample(ctx context.Context, db *sql.DB, runID, directory, phase string) resourceSnapshot {
	now := time.Now()
	result := resourceSnapshot{Phase: phase, Containers: map[string]any{}, Unavailable: map[string]string{"application_pool_waits": "not_exposed_by_production_runtime"}}
	data, err := os.ReadFile("/proc/stat")
	current, parseErr := parseCPU(string(data))
	if err != nil || parseErr != nil {
		result.Unavailable["host_cpu"] = "counters_unavailable"
		s.observed = time.Time{}
	} else {
		busy, wait, available := cpuInterval(s.previous, current)
		if !s.observed.IsZero() && available {
			result.HostCPUPercent, result.HostIOWaitPercent = &busy, &wait
			result.IntervalMS = now.Sub(s.observed).Milliseconds()
		} else {
			result.Unavailable["host_cpu"] = "interval_not_available"
		}
		s.previous, s.observed = current, now
	}
	data, err = os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
				value, parseErr := strconv.ParseUint(fields[1], 10, 64)
				if parseErr == nil {
					value *= 1024
					result.AvailableMemory = &value
				}
			}
		}
	}
	if result.AvailableMemory == nil {
		result.Unavailable["host_memory"] = "counters_unavailable"
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(directory, &disk) == nil {
		free := disk.Bavail * uint64(disk.Bsize)
		result.DiskFree = &free
	} else {
		result.Unavailable["disk"] = "counters_unavailable"
	}
	for _, role := range []string{"server", "postgres"} {
		container := "densemem-eval-" + runID + "-" + role
		label, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "io.dense-mem.eval.run"}}`, container).Output()
		if err != nil || strings.TrimSpace(string(label)) != runID {
			result.Unavailable[role] = "ownership_unverified"
			continue
		}
		data, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{json .}}", container).Output()
		stats, parseErr := parseContainerResources(data)
		if err != nil || parseErr != nil {
			result.Unavailable[role] = "container_stats_unavailable"
		} else {
			result.Containers[role] = stats
		}
	}
	var connections, active, locks int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE state='active'), count(*) FILTER (WHERE wait_event_type='Lock') FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`).Scan(&connections, &active, &locks); err != nil {
		result.Unavailable["postgres_activity"] = "query_unavailable"
	} else {
		result.PGConnections, result.PGActive, result.PGLockWaits = &connections, &active, &locks
	}
	return result
}

func parseContainerResources(data []byte) (map[string]float64, error) {
	var stats struct{ CPUPerc, MemUsage string }
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, err
	}
	cpu, err := strconv.ParseFloat(strings.TrimSuffix(stats.CPUPerc, "%"), 64)
	if err != nil || cpu < 0 || math.IsNaN(cpu) || math.IsInf(cpu, 0) || !strings.HasSuffix(stats.CPUPerc, "%") {
		return nil, fmt.Errorf("container CPU unavailable")
	}
	parts := strings.Split(stats.MemUsage, " / ")
	if len(parts) != 2 || parts[1] == "0B" {
		return nil, fmt.Errorf("container memory unavailable")
	}
	for _, unit := range []struct {
		name  string
		bytes float64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(parts[0], unit.name) {
			memory, err := strconv.ParseFloat(strings.TrimSuffix(parts[0], unit.name), 64)
			if err != nil || memory < 0 || math.IsNaN(memory) || math.IsInf(memory, 0) {
				break
			}
			return map[string]float64{"sampled_cpu_percent_per_core": cpu, "memory_bytes": memory * unit.bytes}, nil
		}
	}
	return nil, fmt.Errorf("container memory unavailable")
}

func startResourceMonitor(ctx context.Context, cancel context.CancelFunc, db *sql.DB, manifest evalharness.Manifest, capture *evalharness.Capture) func() error {
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		sampler := resourceSampler{}
		emit := func(phase string) error {
			sampleCtx, release := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer release()
			return capture.Emit("resources", sampler.sample(sampleCtx, db, manifest.RunID, manifest.SeedDir, phase))
		}
		err := emit("initial")
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for err == nil {
			select {
			case <-stop:
				done <- emit("final")
				return
			case <-ctx.Done():
				done <- emit("final")
				return
			case <-ticker.C:
				err = emit("periodic")
			}
		}
		cancel()
		done <- err
	}()
	return func() error { close(stop); return <-done }
}
