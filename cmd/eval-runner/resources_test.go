package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceCPUInterval(t *testing.T) {
	before, err := parseCPU("cpu 100 20 30 400 50 10 5 1 80 10\ncpu0 0 0 0 0 0 0 0 0")
	require.NoError(t, err)
	require.EqualValues(t, 616, before.total)
	after, err := parseCPU("cpu 150 20 40 430 60 10 5 1 100 10")
	require.NoError(t, err)
	busy, wait, ok := cpuInterval(before, after)
	require.True(t, ok)
	require.InDelta(t, 60, busy, 0.001)
	require.InDelta(t, 10, wait, 0.001)
	for _, invalid := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 3", "cpu 1 2 3 4 invalid 6 7 8"} {
		_, err := parseCPU(invalid)
		require.Error(t, err)
	}
	for _, invalid := range []cpuTimes{before, {total: 10}, {total: before.total + 1, idle: before.idle + 2, wait: before.wait}} {
		_, _, ok := cpuInterval(before, invalid)
		require.False(t, ok)
	}
}

func TestContainerResourcesExposeOnlyNumericMeasurements(t *testing.T) {
	for _, test := range []struct {
		usage string
		want  float64
	}{{"2GiB / 7GiB", 2 << 30}, {"128MiB / 7GiB", 128 << 20}, {"2KiB / 7GiB", 2048}, {"123B / 7GiB", 123}} {
		stats, err := parseContainerResources([]byte(`{"CPUPerc":"150.5%","MemUsage":"` + test.usage + `","Name":"credential-canary","Extra":"https://provider-canary"}`))
		require.NoError(t, err)
		require.Equal(t, map[string]float64{"sampled_cpu_percent_per_core": 150.5, "memory_bytes": test.want}, stats)
	}
	for _, invalid := range []string{`{`, `{}`, `{"CPUPerc":"NaN%","MemUsage":"1MiB / 7GiB"}`, `{"CPUPerc":"1%","MemUsage":"NaNMiB / 7GiB"}`, `{"CPUPerc":"-1%","MemUsage":"1MiB / 7GiB"}`, `{"CPUPerc":"1%","MemUsage":"credential-canary"}`, `{"CPUPerc":"0%","MemUsage":"0B / 0B"}`} {
		_, err := parseContainerResources([]byte(invalid))
		require.Error(t, err)
	}
}
