package main

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GPU is one reading of one NVIDIA GPU. A value the driver does not report
// (fan speed and power limit on many laptops) is negative.
type GPU struct {
	Index    int
	Name     string
	Driver   string
	Util     float64 // percent
	MemUsed  float64 // MiB
	MemTotal float64 // MiB
	Temp     float64 // °C
	Power    float64 // W
	PowerCap float64 // W
	Fan      float64 // percent
}

type gpuMsg struct {
	gpu  GPU
	note string // why there is no GPU data
}

const gpuQuery = "index,name,driver_version,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,fan.speed"

// followGPU reads NVML through nvidia-smi, which ships with the driver. One
// nvidia-smi process stays open and prints a line per GPU every second, so
// nothing is started again and again.
func followGPU(ctx context.Context, send func(gpuMsg)) {
	for {
		err := runGPU(ctx, send)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, exec.ErrNotFound) {
			send(gpuMsg{note: "nvidia-smi not found: no NVIDIA driver on this machine?"})
			return
		}
		note := "nvidia-smi stopped"
		if err != nil {
			note = "nvidia-smi: " + err.Error()
		}
		send(gpuMsg{note: note})
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func runGPU(ctx context.Context, send func(gpuMsg)) error {
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu="+gpuQuery, "--format=csv,noheader,nounits", "-lms", "1000")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errText strings.Builder
	cmd.Stderr = &errText
	if err := cmd.Start(); err != nil {
		return err
	}
	var bad string
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if g, ok := parseGPULine(sc.Text()); ok {
			send(gpuMsg{gpu: g})
		} else if bad == "" {
			bad = strings.TrimSpace(sc.Text()) // nvidia-smi reports its own failures on stdout
		}
	}
	err = cmd.Wait()
	if msg := firstLine(errText.String()); msg != "" {
		return errors.New(msg)
	}
	if bad != "" {
		return errors.New(bad)
	}
	return err
}

// parseGPULine reads one CSV line in the order of gpuQuery.
func parseGPULine(line string) (GPU, bool) {
	f := strings.Split(line, ",")
	if len(f) != 10 {
		return GPU{}, false
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	idx, err := strconv.Atoi(f[0])
	if err != nil {
		return GPU{}, false
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return -1 // "[N/A]", "[Not Supported]"
		}
		return v
	}
	return GPU{
		Index: idx, Name: f[1], Driver: f[2],
		Util: num(f[3]), MemUsed: num(f[4]), MemTotal: num(f[5]),
		Temp: num(f[6]), Power: num(f[7]), PowerCap: num(f[8]), Fan: num(f[9]),
	}, true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
