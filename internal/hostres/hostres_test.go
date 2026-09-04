package hostres

import "testing"

func TestFitsFailsClosedWhenTheProbeIsBroken(t *testing.T) {
	broken := Snapshot{TotalBytes: 0, AvailableBytes: 0, CPUs: 0, OK: false}
	if broken.Fits(4<<30, 2<<30) {
		t.Error("a broken probe must not report room: in a cluster it makes that node win every placement bid")
	}
	if broken.FitsCPU(4, 0) {
		t.Error("a broken probe must not report free cpus")
	}
}

func TestATaskWithNoDeclaredMemoryAlwaysFits(t *testing.T) {
	if !(Snapshot{}).Fits(0, 2<<30) {
		t.Error("a task that declares no memory must not be blocked by a failed probe")
	}
}

func TestFitsRespectsTheHeadroom(t *testing.T) {
	s := Snapshot{TotalBytes: 16 << 30, AvailableBytes: 8 << 30, CPUs: 8, OK: true}
	if !s.Fits(4<<30, 2<<30) {
		t.Error("4GiB should fit in 8GiB with 2GiB headroom")
	}
	if s.Fits(7<<30, 2<<30) {
		t.Error("7GiB must not fit in 8GiB with 2GiB headroom")
	}
}

func TestFitsCPUCountsWhatIsAlreadyRunning(t *testing.T) {
	s := Snapshot{CPUs: 8, TotalBytes: 1, AvailableBytes: 1, OK: true}
	if !s.FitsCPU(4, 4) {
		t.Error("4 + 4 should fit on 8 cpus")
	}
	if s.FitsCPU(4, 5) {
		t.Error("4 + 5 must not fit on 8 cpus; two 4-cpu VMs on an 8-core mini is real oversubscription")
	}
}

func TestProbeReadsTheRealMachine(t *testing.T) {
	s := Probe()
	if !s.OK {
		t.Skip("no sysctl/vm_stat on this machine")
	}
	if s.TotalBytes < 1<<30 || s.CPUs < 1 {
		t.Errorf("probe looks wrong: %+v", s)
	}
	if s.AvailableBytes > s.TotalBytes {
		t.Errorf("available %d exceeds total %d", s.AvailableBytes, s.TotalBytes)
	}
}
