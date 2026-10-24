package hoststats

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestSampleFixtures(t *testing.T) {
	dir1 := filepath.Join("testdata", "1")
	dir2 := filepath.Join("testdata", "2")
	disk := t.TempDir()

	c := NewCollector(dir1, disk)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return t0 }

	s1, err := c.Sample()
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if s1.CPUPercent != 0 || s1.NetRxBps != 0 || s1.NetTxBps != 0 {
		t.Fatalf("first call rates must be 0: %+v", s1)
	}
	if s1.Load1 != 0.50 {
		t.Fatalf("Load1 = %v", s1.Load1)
	}
	wantMemTotal := uint64(2048000) * 1024
	wantMemAvail := uint64(1024000) * 1024
	if s1.MemTotal != wantMemTotal || s1.MemAvailable != wantMemAvail {
		t.Fatalf("mem: total=%d avail=%d", s1.MemTotal, s1.MemAvailable)
	}
	if s1.DiskTotal == 0 {
		t.Fatal("DiskTotal = 0")
	}

	c.procRoot = dir2
	t1 := t0.Add(2 * time.Second)
	c.now = func() time.Time { return t1 }

	s2, err := c.Sample()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if s2.Load1 != 1.25 {
		t.Fatalf("Load1 = %v", s2.Load1)
	}
	// CPU: snap1 total=9700 busy=1500; snap2 total=10600 busy=2200
	// dtotal=900 dbusy=700 -> 100*700/900
	wantCPU := 100 * 700.0 / 900.0
	if math.Abs(s2.CPUPercent-wantCPU) > 1e-9 {
		t.Fatalf("CPUPercent = %v, want %v", s2.CPUPercent, wantCPU)
	}
	// net excluding lo: rx 1.5M->4.5M, tx 2.7M->5.7M over 2s
	wantRx := 3_000_000.0 / 2
	wantTx := 3_000_000.0 / 2
	if math.Abs(s2.NetRxBps-wantRx) > 1e-6 || math.Abs(s2.NetTxBps-wantTx) > 1e-6 {
		t.Fatalf("net rates rx=%v tx=%v, want %v %v", s2.NetRxBps, s2.NetTxBps, wantRx, wantTx)
	}
}

func TestCounterReset(t *testing.T) {
	dir2 := filepath.Join("testdata", "2")
	dir3 := filepath.Join("testdata", "3")
	disk := t.TempDir()

	c := NewCollector(dir2, disk)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return t0 }
	if _, err := c.Sample(); err != nil {
		t.Fatal(err)
	}

	c.procRoot = dir3
	t1 := t0.Add(time.Second)
	c.now = func() time.Time { return t1 }
	s, err := c.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if s.NetRxBps != 0 || s.NetTxBps != 0 {
		t.Fatalf("reset should zero rates: rx=%v tx=%v", s.NetRxBps, s.NetTxBps)
	}
	// CPU also went... actually CPU counters increased in snap3 vs snap2, that's fine.
	// For CPU reset, force prev higher by sampling again from an older snap.
}

func TestCPUReset(t *testing.T) {
	dir2 := filepath.Join("testdata", "2")
	dir1 := filepath.Join("testdata", "1")
	disk := t.TempDir()

	c := NewCollector(dir2, disk)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return t0 }
	if _, err := c.Sample(); err != nil {
		t.Fatal(err)
	}

	c.procRoot = dir1 // earlier counters
	t1 := t0.Add(time.Second)
	c.now = func() time.Time { return t1 }
	s, err := c.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUPercent != 0 {
		t.Fatalf("CPU reset should be 0, got %v", s.CPUPercent)
	}
}

func TestLoExcluded(t *testing.T) {
	// Snapshot 1 has huge lo counters; if included, rates from 1->2 would be wrong.
	// Already covered by TestSampleFixtures expected rates; assert explicitly that
	// reading net from snap1 excludes lo.
	c := NewCollector(filepath.Join("testdata", "1"), t.TempDir())
	rx, tx, err := c.readNet()
	if err != nil {
		t.Fatal(err)
	}
	if rx != 1_500_000 || tx != 2_700_000 {
		t.Fatalf("lo not excluded: rx=%d tx=%d", rx, tx)
	}
}

func TestRealProc(t *testing.T) {
	disk := t.TempDir()
	c := NewCollector("/proc", disk)
	s1, err := c.Sample()
	if err != nil {
		t.Fatalf("sample1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	s2, err := c.Sample()
	if err != nil {
		t.Fatalf("sample2: %v", err)
	}
	for _, s := range []Sample{s1, s2} {
		if s.CPUPercent < 0 || s.CPUPercent > 100 {
			t.Fatalf("CPUPercent out of range: %v", s.CPUPercent)
		}
		if s.Load1 < 0 {
			t.Fatalf("Load1 < 0: %v", s.Load1)
		}
		if s.MemTotal == 0 {
			t.Fatal("MemTotal = 0")
		}
		if s.NetRxBps < 0 || s.NetTxBps < 0 {
			t.Fatalf("negative net rates: %+v", s)
		}
		if s.DiskTotal == 0 {
			t.Fatal("DiskTotal = 0")
		}
	}
}

func TestCPUPercentBusyGoingBackwardsIsZero(t *testing.T) {
	prev := cpuTimes{total: 1000, idle: 100, iowait: 0}
	cur := cpuTimes{total: 1100, idle: 250, iowait: 0} // busy 900 -> 850
	if got := cpuPercent(prev, cur); got != 0 {
		t.Fatalf("cpuPercent = %v, want 0", got)
	}
}
