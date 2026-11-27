//go:build unix

package record

import "syscall"

func lowerPriority(pid int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, ffmpegNice)
}
