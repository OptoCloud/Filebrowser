package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ContainerName identifies the AppContainer; its SID is derived from it.
const ContainerName = "filebrowser.media"

var (
	userenv                 = windows.NewLazySystemDLL("userenv.dll")
	procDeriveContainerSid  = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	ntdll                   = windows.NewLazySystemDLL("ntdll.dll")
	procNtCreateLowBoxToken = ntdll.NewProc("NtCreateLowBoxToken")
	procNtResumeProcess     = ntdll.NewProc("NtResumeProcess")
)

const processSuspendResume = 0x0800

func launch(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox: no command")
		return 125
	}
	code, err := run(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 125
	}
	return code
}

func run(args []string) (int, error) {
	sid, err := ContainerSID()
	if err != nil {
		return 0, err
	}
	writeDir := os.Getenv(EnvWrite)
	if writeDir != "" {
		if err := grant(writeDir, sid, windows.GENERIC_ALL, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT); err != nil {
			return 0, fmt.Errorf("grant %s: %w", writeDir, err)
		}
	}
	// The executable must be readable by the container. Binaries under
	// Program Files already are; elsewhere grant it (best effort: this fails
	// when the service account does not own the file).
	_ = grant(args[0], sid, windows.GENERIC_READ|windows.GENERIC_EXECUTE, windows.NO_INHERITANCE)

	token, err := lowBoxToken(sid)
	if err != nil {
		return 0, err
	}
	defer token.Close()
	job, err := newJob()
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job) // KILL_ON_JOB_CLOSE: the child dies with us

	cmd := exec.Command(args[0], args[1:]...) // #nosec G204 G702 -- argv comes from the media runner in the parent process; whoever can start the launcher can already run programs
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot")}
	if writeDir != "" {
		cmd.Env = append(cmd.Env, "TEMP="+writeDir, "TMP="+writeDir)
	}
	// Start suspended so the process runs no code before it is in the job.
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token), CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w (the AppContainer needs read access to it: icacls %q /grant \"*S-1-15-2-1:RX\")", args[0], err, args[0])
	}
	pid := uint32(cmd.Process.Pid) // #nosec G115 -- process IDs are positive 32-bit values
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|processSuspendResume, false, pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return 0, err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.TerminateProcess(h, 1)
		return 0, fmt.Errorf("assign job: %w", err)
	}
	if st, _, _ := procNtResumeProcess.Call(uintptr(h)); st != 0 {
		_ = windows.TerminateProcess(h, 1)
		return 0, fmt.Errorf("NtResumeProcess: NTSTATUS 0x%x", st)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// ContainerSID returns the AppContainer SID for ContainerName.
func ContainerSID() (*windows.SID, error) {
	name, err := windows.UTF16PtrFromString(ContainerName)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	// #nosec G103 -- Win32 call with pointers to live Go values
	if hr, _, _ := procDeriveContainerSid.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid))); hr != 0 {
		return nil, fmt.Errorf("DeriveAppContainerSidFromAppContainerName: HRESULT 0x%x", hr)
	}
	return sid, nil
}

// lowBoxToken derives an AppContainer token without capabilities (so without
// network access) from this process's token, as Chromium's sandbox does.
func lowBoxToken(sid *windows.SID) (windows.Token, error) {
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &self); err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer self.Close()
	var lb windows.Token
	st, _, _ := procNtCreateLowBoxToken.Call(uintptr(unsafe.Pointer(&lb)), uintptr(self), uintptr(windows.TOKEN_ALL_ACCESS), // #nosec G103 -- Win32 call
		0, uintptr(unsafe.Pointer(sid)), 0, 0, 0, 0) // #nosec G103 -- Win32 call
	if st != 0 {
		return 0, fmt.Errorf("NtCreateLowBoxToken: NTSTATUS 0x%x", st)
	}
	return lb, nil
}

func newJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if n, _ := strconv.ParseUint(os.Getenv(EnvMem), 10, 64); n > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(n)
	}
	if n, _ := strconv.ParseInt(os.Getenv(EnvCPU), 10, 64); n > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_TIME
		info.BasicLimitInformation.PerJobUserTimeLimit = n * 10_000_000 // 100 ns units
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { // #nosec G103 -- Win32 call
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("job limits: %w", err)
	}
	ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: windows.JOB_OBJECT_UILIMIT_DESKTOP |
		windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS | windows.JOB_OBJECT_UILIMIT_EXITWINDOWS |
		windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES |
		windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS |
		windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions,
		uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil { // #nosec G103 -- Win32 call
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("job UI limits: %w", err)
	}
	return job, nil
}

// grant adds an allow ACE for sid to path's DACL.
func grant(path string, sid *windows.SID, mask windows.ACCESS_MASK, inherit uint32) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
