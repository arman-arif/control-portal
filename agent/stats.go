package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// cpuSample is the cumulative jiffies from the first line of /proc/stat.
type cpuSample struct {
	idle, total uint64
}

func readCPUSample(path string) (cpuSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return cpuSample{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuSample{}, fmt.Errorf("empty %s", path)
	}
	return parseCPULine(sc.Text())
}

func parseCPULine(line string) (cpuSample, error) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}, fmt.Errorf("unexpected cpu line %q", line)
	}
	var s cpuSample
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuSample{}, err
		}
		// guest and guest_nice (fields 9 and 10) are already counted in user and nice.
		if i >= 8 {
			break
		}
		s.total += v
		if i == 3 || i == 4 { // idle, iowait
			s.idle += v
		}
	}
	return s, nil
}

func cpuPercent(prev, cur cpuSample) float64 {
	dt := cur.total - prev.total
	if cur.total <= prev.total || dt == 0 {
		return 0
	}
	busy := dt - (cur.idle - prev.idle)
	return float64(busy) / float64(dt) * 100
}

// parseMeminfo returns total and used memory in bytes, where used excludes
// cache and buffers (MemTotal - MemAvailable).
func parseMeminfo(content string) (total, used uint64, err error) {
	var avail uint64
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, perr := strconv.ParseUint(fields[1], 10, 64)
		if perr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = v*1024, true
		case "MemAvailable:":
			avail, haveAvail = v*1024, true
		}
	}
	if !haveTotal || !haveAvail {
		return 0, 0, fmt.Errorf("meminfo missing MemTotal or MemAvailable")
	}
	if avail > total {
		avail = total
	}
	return total, total - avail, nil
}

func parseLoadavg(content string) (l1, l5, l15 float64, err error) {
	f := strings.Fields(content)
	if len(f) < 3 {
		return 0, 0, 0, fmt.Errorf("unexpected loadavg %q", content)
	}
	if l1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return
	}
	if l5, err = strconv.ParseFloat(f[1], 64); err != nil {
		return
	}
	l15, err = strconv.ParseFloat(f[2], 64)
	return
}

func parseUptime(content string) (uint64, error) {
	f := strings.Fields(content)
	if len(f) < 1 {
		return 0, fmt.Errorf("unexpected uptime %q", content)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return uint64(v), err
}

// statsCollector keeps the previous CPU sample so each Collect reports usage
// since the last call.
type statsCollector struct {
	prev cpuSample
}

func (c *statsCollector) Collect() (protocol.Stats, error) {
	s := protocol.Stats{Time: time.Now().Unix()}

	cur, err := readCPUSample("/proc/stat")
	if err != nil {
		return s, err
	}
	if c.prev.total != 0 {
		s.CPUPercent = cpuPercent(c.prev, cur)
	}
	c.prev = cur

	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.MemTotal, s.MemUsed, _ = parseMeminfo(string(b))
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		s.Load1, s.Load5, s.Load15, _ = parseLoadavg(string(b))
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		s.Uptime, _ = parseUptime(string(b))
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		bs := uint64(fs.Bsize)
		s.DiskTotal = fs.Blocks * bs
		s.DiskUsed = (fs.Blocks - fs.Bfree) * bs
	}
	return s, nil
}
