//go:build darwin

// Darwin (macOS) is a limited fallback for development. A full Darwin metric
// backend (host_statistics/vm_stat/netstat, launchd discovery) is planned.
// For now this reports host info and real disk usage; everything not yet
// implemented returns ok=false so it marshals to JSON null rather than
// a misleading 0.

package collect

import (
	"os"
	"runtime"
	"syscall"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

func readHost() model.Host {
	name, _ := os.Hostname()
	kernel, _ := syscall.Sysctl("kern.osrelease")
	// UptimeSeconds is left nil (not yet implemented on Darwin) -> null.
	return model.Host{
		Name:     name,
		OS:       "macOS",
		Kernel:   kernel,
		Arch:     runtime.GOARCH,
		Platform: "darwin (limited)",
	}
}

// readMem is not yet implemented on Darwin (needs host_statistics/vm_stat;
// stdlib syscall.Sysctl truncates the 64-bit hw.memsize value at null bytes).
func readMem() (usedGB, totalGB float64, ok bool) { return 0, 0, false }

// readDisk is real on Darwin via statfs.
func readDisk(path string) (usedGB, totalGB float64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := float64(st.Bsize)
	total := float64(st.Blocks) * bs
	free := float64(st.Bavail) * bs
	return (total - free) / 1e9, total / 1e9, true
}

// CPU and load sampling are not yet implemented on Darwin.
func readCPUSample() (busy, total uint64, ok bool)    { return 0, 0, false }
func readLoad() (one, five, fifteen float64, ok bool) { return 0, 0, 0, false }
