//go:build windows

package runner

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsProcessJobs = struct {
	sync.Mutex
	handles map[int]windows.Handle
}{handles: make(map[int]windows.Handle)}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// registerProcessTree places the agent process in a kill-on-close Job Object.
// Child processes inherit the job, which gives cancellation and timeout the
// same process-tree semantics as a Unix process group.
func registerProcessTree(process *os.Process) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	closeJob := true
	defer func() {
		if closeJob {
			_ = windows.CloseHandle(job)
		}
	}()

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return err
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(processHandle)
	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		return err
	}

	windowsProcessJobs.Lock()
	windowsProcessJobs.handles[process.Pid] = job
	windowsProcessJobs.Unlock()
	closeJob = false
	return nil
}

func terminateProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	windowsProcessJobs.Lock()
	job, ok := windowsProcessJobs.handles[process.Pid]
	if ok {
		delete(windowsProcessJobs.handles, process.Pid)
	}
	windowsProcessJobs.Unlock()
	if ok {
		err := windows.TerminateJobObject(job, uint32(terminatedExitCode()))
		_ = windows.CloseHandle(job)
		if err == nil {
			return nil
		}
	}

	// A process can already belong to a non-breakaway job imposed by its host.
	// taskkill is the bounded fallback for that case and still targets children.
	command := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Run(); err == nil {
		return nil
	}
	if err := process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	} else {
		return err
	}
}

func processExitCode(state *os.ProcessState) int {
	if exitCode := state.ExitCode(); exitCode >= 0 {
		return exitCode
	}
	return 1
}

func terminatedExitCode() int {
	return 137
}
