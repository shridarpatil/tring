package record

import (
	"bytes"
	"os/exec"
)

// ffmpegNice is the scheduling priority recording ffmpeg runs at. Encodes run
// in the gateway's container next to live call media; at nice 10 they only get
// CPU the live calls leave idle, so a long video compose can't make calls choppy.
const ffmpegNice = 10

// runLowPriority is cmd.CombinedOutput with the process at ffmpegNice.
func runLowPriority(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := startLowPriority(cmd); err != nil {
		return nil, err
	}
	err := cmd.Wait()
	return out.Bytes(), err
}

// startLowPriority starts cmd and lowers its priority at once. Linux applies
// nice per thread and new threads inherit their creator's, so this must land
// before ffmpeg spawns its codec threads: it does, as ffmpeg only creates them
// after loading its libraries and opening every input. Failing to lower it is
// not fatal: the recording still gets made, just at normal priority.
func startLowPriority(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	lowerPriority(cmd.Process.Pid)
	return nil
}
