package observability

import (
	"context"
	"maps"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

type networkCounter struct{ RX, TX uint64 }

type runtimeReading struct {
	Mode, Instance, Distribution, Kernel, MemoryScope string
	CPUCapacity                                       float64
	CPUSeconds                                        *float64
	ProcessMemory, MemoryTotal, MemoryUsed            *uint64
	StoragePercent                                    *float64
	Network                                           map[string]networkCounter
}

// 采样由观测服务唯一持有；按需刷新，不创建后台循环或阻塞等待差分。
type runtimeSampler struct {
	mu       sync.Mutex
	options  Options
	read     func(context.Context) runtimeReading
	at       time.Time
	previous runtimeReading
	cache    map[string]any
}

func (s *runtimeSampler) snapshot(ctx context.Context, now time.Time) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && now.Sub(s.at) >= 0 && now.Sub(s.at) < time.Second {
		return maps.Clone(s.cache)
	}
	reading := s.read(ctx)
	view := map[string]any{
		"runtime_mode": reading.Mode, "runtime_label": "Go " + strings.TrimPrefix(runtime.Version(), "go"),
		"instance_name": reading.Instance, "distribution": reading.Distribution, "kernel_version": reading.Kernel,
		"architecture": runtime.GOARCH, "go_version": strings.TrimPrefix(runtime.Version(), "go"), "cpu_capacity": reading.CPUCapacity,
		"service_started_at": stamp(s.options.Started), "service_uptime_seconds": max(0, int64(now.Sub(s.options.Started).Seconds())),
		"process_cpu_percent": nil, "process_memory_bytes": nil, "process_memory_percent": memoryPercentage(reading.ProcessMemory, reading.MemoryTotal),
		"memory_scope": reading.MemoryScope, "memory_percent": memoryPercentage(reading.MemoryUsed, reading.MemoryTotal),
		"storage_percent": nil, "network_rx_bytes_per_sec": nil, "network_tx_bytes_per_sec": nil,
	}
	if reading.ProcessMemory != nil {
		view["process_memory_bytes"] = *reading.ProcessMemory
	}
	if reading.StoragePercent != nil {
		view["storage_percent"] = percent(*reading.StoragePercent)
	}
	elapsed := now.Sub(s.at).Seconds()
	if !s.at.IsZero() && elapsed > 0 {
		if reading.CPUSeconds != nil && s.previous.CPUSeconds != nil && reading.CPUCapacity > 0 {
			delta := *reading.CPUSeconds - *s.previous.CPUSeconds
			if delta >= 0 {
				view["process_cpu_percent"] = percent(delta * 100 / elapsed / reading.CPUCapacity)
			}
		}
		view["network_rx_bytes_per_sec"], view["network_tx_bytes_per_sec"] = networkRates(s.previous.Network, reading.Network, elapsed)
	}
	// 已取消的请求不替其他消费者覆盖缓存或差分基线。
	if ctx.Err() == nil {
		s.previous, s.at, s.cache = reading, now, maps.Clone(view)
	}
	return view
}

func memoryPercentage(used, total *uint64) any {
	if used == nil || total == nil || *total == 0 {
		return nil
	}
	return percent(float64(*used) * 100 / float64(*total))
}

func percent(value float64) any {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return math.Round(max(0, min(100, value))*10) / 10
}

func networkRates(previous, current map[string]networkCounter, elapsed float64) (any, any) {
	if previous == nil || current == nil || len(previous) != len(current) || elapsed <= 0 {
		return nil, nil
	}
	var rx, tx float64
	for name, counter := range current {
		before, ok := previous[name]
		if !ok || counter.RX < before.RX || counter.TX < before.TX {
			// 网卡新增/移除或计数器重置时重新建立基线，避免虚假流量。
			return nil, nil
		}
		rx += float64(counter.RX - before.RX)
		tx += float64(counter.TX - before.TX)
	}
	return math.Round(rx/elapsed*10) / 10, math.Round(tx/elapsed*10) / 10
}

func readRuntime(parent context.Context, directory string) runtimeReading {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	r := runtimeReading{Mode: "native", Distribution: runtime.GOOS, Kernel: "unknown", CPUCapacity: float64(runtime.NumCPU()), MemoryScope: "system"}
	r.Instance, _ = os.Hostname()
	if platform, _, version, err := host.PlatformInformationWithContext(ctx); err == nil && platform != "" {
		r.Distribution = strings.TrimSpace(platform + " " + version)
	}
	if kernel, err := host.KernelVersionWithContext(ctx); err == nil && kernel != "" {
		r.Kernel = kernel
	}
	if memory, err := mem.VirtualMemoryWithContext(ctx); err == nil && memory.Total > 0 {
		total, used := memory.Total, memory.Total-min(memory.Total, memory.Available)
		r.MemoryTotal, r.MemoryUsed = &total, &used
	}
	if runtime.GOOS == "linux" && (regularFile("/.dockerenv") || regularFile("/run/.containerenv")) {
		r.Mode, r.MemoryScope = "docker", "visible"
		applyCgroup("/sys/fs/cgroup", &r)
	}
	if p, err := process.NewProcessWithContext(ctx, int32(os.Getpid())); err == nil {
		if times, err := p.TimesWithContext(ctx); err == nil {
			seconds := times.User + times.System
			r.CPUSeconds = &seconds
		}
		if memory, err := p.MemoryInfoWithContext(ctx); err == nil {
			r.ProcessMemory = &memory.RSS
		}
	}
	if directory != "" {
		if usage, err := disk.UsageWithContext(ctx, directory); err == nil && usage.Total > 0 {
			// 与 Python 一致，使用实际已用块，不把当前用户保留块当成已用。
			used := float64(usage.Used) * 100 / float64(usage.Total)
			r.StoragePercent = &used
		}
	}
	if interfaces, err := net.Interfaces(); err == nil {
		if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
			visible := make(map[string]bool, len(interfaces))
			for _, nic := range interfaces {
				visible[nic.Name] = nic.Flags&net.FlagLoopback == 0 && nic.Flags&net.FlagUp != 0
			}
			r.Network = map[string]networkCounter{}
			for _, counter := range counters {
				if visible[counter.Name] {
					r.Network[counter.Name] = networkCounter{RX: counter.BytesRecv, TX: counter.BytesSent}
				}
			}
		}
	}
	return r
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// cgroup 只调整当前可见资源口径，不改变领域存储或服务配置。
func applyCgroup(root string, r *runtimeReading) {
	for _, pair := range [][2]string{
		{"memory.max", "memory.current"},
		{"memory/memory.limit_in_bytes", "memory/memory.usage_in_bytes"},
		{"memory.limit_in_bytes", "memory.usage_in_bytes"},
	} {
		limit, limitOK := cgroupNumber(root, pair[0])
		used, usedOK := cgroupNumber(root, pair[1])
		if limitOK && usedOK && limit > 0 && limit < 1<<60 && (r.MemoryTotal == nil || limit <= *r.MemoryTotal) {
			r.MemoryTotal, r.MemoryUsed, r.MemoryScope = &limit, &used, "container"
			break
		}
	}
	var quota, period uint64
	if data, err := os.ReadFile(filepath.Join(root, "cpu.max")); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) == 2 {
			quota, _ = strconv.ParseUint(parts[0], 10, 64)
			period, _ = strconv.ParseUint(parts[1], 10, 64)
		}
	}
	if quota == 0 || period == 0 {
		for _, prefix := range []string{"cpu", "cpu,cpuacct", ""} {
			quota, _ = cgroupNumber(root, filepath.Join(prefix, "cpu.cfs_quota_us"))
			period, _ = cgroupNumber(root, filepath.Join(prefix, "cpu.cfs_period_us"))
			if quota > 0 && period > 0 {
				break
			}
		}
	}
	if quota > 0 && period > 0 {
		r.CPUCapacity = math.Round(max(0.01, min(r.CPUCapacity, float64(quota)/float64(period)))*100) / 100
	}
}

func cgroupNumber(root, name string) (uint64, bool) {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return value, err == nil
}
