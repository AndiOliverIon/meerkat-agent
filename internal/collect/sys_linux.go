//go:build linux

package collect

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

func readHost() model.Host {
	name, _ := os.Hostname()
	h := model.Host{
		Name:     name,
		OS:       prettyOS(),
		Kernel:   kernelRelease(),
		Arch:     runtime.GOARCH,
		Platform: "linux",
	}
	if up, ok := uptimeSeconds(); ok {
		h.UptimeSeconds = &up
	}
	return h
}

func prettyOS() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "Linux"
}

func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	return charsToString(u.Release[:])
}

func charsToString(ca []int8) string {
	b := make([]byte, 0, len(ca))
	for _, c := range ca {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// uptimeSeconds returns (uptime, true) or (0, false) if /proc/uptime is
// unreadable.
func uptimeSeconds() (uint64, bool) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return uint64(f), true
}

// readMem returns used and total memory in GB from /proc/meminfo. ok is false
// if the file can't be read or reports no total.
func readMem() (usedGB, totalGB float64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var totalKB, availKB float64
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(fields[1], 64)
		switch fields[0] {
		case "MemTotal:":
			totalKB = v
		case "MemAvailable:":
			availKB = v
		}
	}
	if totalKB == 0 {
		return 0, 0, false
	}
	totalGB = totalKB / 1024 / 1024
	usedGB = (totalKB - availKB) / 1024 / 1024
	return usedGB, totalGB, true
}

// readDisk returns df-style used space, total space, and use percent for the
// filesystem at path. ok is false if statfs fails.
func readDisk(path string) (usedGB, totalGB, percent float64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, false
	}
	usedGB, totalGB, percent = diskUsageFromStatfs(
		float64(st.Blocks),
		float64(st.Bfree),
		float64(st.Bavail),
		float64(st.Bsize),
	)
	return usedGB, totalGB, percent, true
}

// readLoad returns the 1/5/15 minute load averages from /proc/loadavg.
func readLoad() (one, five, fifteen float64, ok bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	one, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, false
	}
	five, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, false
	}
	fifteen, err = strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return one, five, fifteen, true
}

// readCPUSample returns cumulative busy and total CPU jiffies from /proc/stat.
// ok is false if /proc/stat can't be read or parsed.
func readCPUSample() (busy, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var idle uint64
	for i, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return total - idle, total, true
}
