// Package hoststats collects host metrics from /proc and statfs.
package hoststats

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Sample is one reading. Rates are per second since the previous Sample call;
// they are 0 on the first call and when a counter went backwards (reset).
type Sample struct {
	At                     time.Time
	CPUPercent             float64 // 0..100 across all CPUs, from /proc/stat "cpu" line deltas (busy = total - idle - iowait)
	Load1                  float64 // /proc/loadavg first field
	MemTotal, MemAvailable uint64  // bytes, /proc/meminfo MemTotal and MemAvailable (kB * 1024)
	DiskTotal, DiskUsed    uint64  // bytes, statfs on diskPath: total = Blocks*Bsize, used = (Blocks-Bfree)*Bsize
	NetRxBps, NetTxBps     float64 // bytes/s summed over /proc/net/dev interfaces except "lo"
}

// Collector reads host metrics from a proc filesystem and a disk path.
type Collector struct {
	procRoot string
	diskPath string
	now      func() time.Time

	hasPrev bool
	prevAt  time.Time
	prevCPU cpuTimes
	prevRx  uint64
	prevTx  uint64
}

type cpuTimes struct {
	total  uint64
	idle   uint64
	iowait uint64
}

// NewCollector reads from procRoot (normally "/proc") and measures the
// filesystem holding diskPath.
func NewCollector(procRoot, diskPath string) *Collector {
	return &Collector{
		procRoot: procRoot,
		diskPath: diskPath,
		now:      time.Now,
	}
}

// Sample takes a reading. Not safe for concurrent use.
func (c *Collector) Sample() (Sample, error) {
	at := c.now()
	s := Sample{At: at}

	cpu, err := c.readCPU()
	if err != nil {
		return Sample{}, err
	}
	load1, err := c.readLoad1()
	if err != nil {
		return Sample{}, err
	}
	s.Load1 = load1

	memTotal, memAvail, err := c.readMem()
	if err != nil {
		return Sample{}, err
	}
	s.MemTotal = memTotal
	s.MemAvailable = memAvail

	diskTotal, diskUsed, err := c.readDisk()
	if err != nil {
		return Sample{}, err
	}
	s.DiskTotal = diskTotal
	s.DiskUsed = diskUsed

	rx, tx, err := c.readNet()
	if err != nil {
		return Sample{}, err
	}

	if c.hasPrev {
		dt := at.Sub(c.prevAt).Seconds()
		if dt > 0 {
			s.CPUPercent = cpuPercent(c.prevCPU, cpu)
			if rx >= c.prevRx {
				s.NetRxBps = float64(rx-c.prevRx) / dt
			}
			if tx >= c.prevTx {
				s.NetTxBps = float64(tx-c.prevTx) / dt
			}
		}
	}

	c.hasPrev = true
	c.prevAt = at
	c.prevCPU = cpu
	c.prevRx = rx
	c.prevTx = tx
	return s, nil
}

func cpuPercent(prev, cur cpuTimes) float64 {
	dtotal := cur.total - prev.total
	if cur.total < prev.total || dtotal == 0 {
		return 0
	}
	// Unsigned arithmetic: compare before subtracting so a busy count that
	// went backwards gives 0 instead of wrapping around to 100%.
	curBusy := cur.total - cur.idle - cur.iowait
	prevBusy := prev.total - prev.idle - prev.iowait
	if curBusy < prevBusy {
		return 0
	}
	dbusy := curBusy - prevBusy
	pct := 100 * float64(dbusy) / float64(dtotal)
	if pct > 100 {
		pct = 100
	}
	return pct
}

func (c *Collector) readCPU() (cpuTimes, error) {
	f, err := os.Open(filepath.Join(c.procRoot, "stat"))
	if err != nil {
		return cpuTimes{}, fmt.Errorf("hoststats: open stat: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal ...
		if len(fields) < 5 {
			return cpuTimes{}, fmt.Errorf("hoststats: short cpu line")
		}
		var vals []uint64
		for _, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("hoststats: parse cpu: %w", err)
			}
			vals = append(vals, v)
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		idle := vals[3]
		var iowait uint64
		if len(vals) > 4 {
			iowait = vals[4]
		}
		return cpuTimes{total: total, idle: idle, iowait: iowait}, nil
	}
	if err := sc.Err(); err != nil {
		return cpuTimes{}, fmt.Errorf("hoststats: read stat: %w", err)
	}
	return cpuTimes{}, fmt.Errorf("hoststats: no cpu line in stat")
}

func (c *Collector) readLoad1() (float64, error) {
	b, err := os.ReadFile(filepath.Join(c.procRoot, "loadavg"))
	if err != nil {
		return 0, fmt.Errorf("hoststats: read loadavg: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, fmt.Errorf("hoststats: short loadavg")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("hoststats: parse loadavg: %w", err)
	}
	return v, nil
}

func (c *Collector) readMem() (total, available uint64, err error) {
	f, err := os.Open(filepath.Join(c.procRoot, "meminfo"))
	if err != nil {
		return 0, 0, fmt.Errorf("hoststats: open meminfo: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			available = kb * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("hoststats: read meminfo: %w", err)
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("hoststats: MemTotal missing")
	}
	return total, available, nil
}

func (c *Collector) readDisk() (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(c.diskPath, &st); err != nil {
		return 0, 0, fmt.Errorf("hoststats: statfs: %w", err)
	}
	bsize := uint64(st.Bsize) //nolint:gosec // Bsize is positive filesystem block size
	total = st.Blocks * bsize
	used = (st.Blocks - st.Bfree) * bsize
	return total, used, nil
}

func (c *Collector) readNet() (rx, tx uint64, err error) {
	f, err := os.Open(filepath.Join(c.procRoot, "net", "dev"))
	if err != nil {
		return 0, 0, fmt.Errorf("hoststats: open net/dev: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if lineNo <= 2 {
			continue // header lines
		}
		line := strings.TrimSpace(sc.Text())
		// "eth0: bytes packets ..."
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		// rx bytes is field 0, tx bytes is field 8
		if len(fields) < 9 {
			continue
		}
		r, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("hoststats: parse net rx: %w", err)
		}
		t, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("hoststats: parse net tx: %w", err)
		}
		rx += r
		tx += t
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("hoststats: read net/dev: %w", err)
	}
	return rx, tx, nil
}
