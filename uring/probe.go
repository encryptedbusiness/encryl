//go:build linux

package uring

import (
	"fmt"

	"github.com/encryptedbusiness/encryl/uring/giouring"
	"golang.org/x/sys/unix"
)

// MinimumKernel is the oldest kernel the reactor accepts.
var MinimumKernel = struct{ Major, Minor int }{6, 6}

// ProbeReport is the structured result of the kernel feature gate.
type ProbeReport struct {
	Kernel     string
	KernelOK   bool
	Opcodes    map[string]bool
	BufferRing bool
	Problems   []string
}

// Ready reports whether every mandatory feature is present.
func (p ProbeReport) Ready() bool { return len(p.Problems) == 0 }

// Probe checks the kernel release and the opcodes the reactor requires. It
// creates and tears down a throwaway ring.
func Probe() ProbeReport {
	report := ProbeReport{Opcodes: map[string]bool{}}

	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		report.Problems = append(report.Problems, "uname: "+err.Error())
		return report
	}
	report.Kernel = unix.ByteSliceToString(u.Release[:])

	major, minor, err := parseKernel(report.Kernel)
	if err != nil {
		report.Problems = append(report.Problems, err.Error())
	} else if major < MinimumKernel.Major || (major == MinimumKernel.Major && minor < MinimumKernel.Minor) {
		report.Problems = append(report.Problems,
			fmt.Sprintf("kernel %s is older than %d.%d", report.Kernel, MinimumKernel.Major, MinimumKernel.Minor))
	} else {
		report.KernelOK = true
	}

	probe, err := giouring.GetProbe()
	if err != nil {
		report.Problems = append(report.Problems, "io_uring_get_probe: "+err.Error())
		return report
	}
	for name, op := range map[string]uint8{
		"accept":          giouring.OpAccept,
		"poll":            giouring.OpPollAdd,
		"recv":            giouring.OpRecv,
		"send":            giouring.OpSend,
		"close":           giouring.OpClose,
		"cancel":          giouring.OpAsyncCancel,
		"provide_buffers": giouring.OpProvideBuffers,
		"read":            giouring.OpRead,
	} {
		ok := probe.IsSupported(op)
		report.Opcodes[name] = ok
		if !ok {
			report.Problems = append(report.Problems, "opcode not supported: "+name)
		}
	}

	ring := giouring.NewRing()
	if err := ring.QueueInit(64, giouring.SetupSingleIssuer|giouring.SetupRDisabled); err != nil {
		report.Problems = append(report.Problems, "ring setup: "+err.Error())
		return report
	}
	defer ring.QueueExit()
	if _, err := ring.RegisterRestrictions(Restrictions()); err != nil {
		report.Problems = append(report.Problems, "restrictions: "+err.Error())
	}
	return report
}

func parseKernel(release string) (int, int, error) {
	var major, minor int
	if _, err := fmt.Sscanf(release, "%d.%d", &major, &minor); err != nil {
		return 0, 0, fmt.Errorf("cannot parse kernel release %q", release)
	}
	return major, minor, nil
}
