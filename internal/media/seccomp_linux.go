package media

import (
	"encoding/binary"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// The sandbox's seccomp filter. bubblewrap gives ffmpeg its own namespaces
// and an empty view of the system; this takes from it the system calls with
// which a compromised decoder would work on getting out of that: making
// further namespaces (where it would be root and could reach kernel
// interfaces closed to it otherwise), mounting, tracing, and the larger
// kernel subsystems ffmpeg has no use for.

// seccompDenied are refused with EPERM.
var seccompDenied = []uintptr{
	unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
	unix.SYS_OPEN_TREE, unix.SYS_MOVE_MOUNT, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
	unix.SYS_MOUNT_SETATTR, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
	unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
	unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_REBOOT, unix.SYS_ACCT,
	// No network exists in the sandbox; without sockets there is no netlink either.
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR,
}

// cloneNamespaces are the clone flags that make a namespace.
const cloneNamespaces = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
	unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWTIME

// seccompArch is the audit architecture of this build, or 0 where no filter
// is made.
func seccompArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	}
	return 0
}

// seccompFilter assembles the filter as the classic BPF program bubblewrap's
// --seccomp takes, or nil where there is none for this architecture.
func seccompFilter() []byte {
	arch := seccompArch()
	if arch == 0 {
		return nil
	}
	const (
		allow  = unix.SECCOMP_RET_ALLOW
		eperm  = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
		enosys = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)
		// Offsets in struct seccomp_data.
		offNr, offArch, offArg0 = 0, 4, 16
	)
	type ins struct {
		code   uint16
		k      uint32
		jt, jf string // labels; "" falls through
	}
	ld := func(off uint32) ins { return ins{code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, k: off} }
	ret := func(v uint32) ins { return ins{code: unix.BPF_RET | unix.BPF_K, k: v} }
	jeq := func(k uint32, jt, jf string) ins {
		return ins{code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, k: k, jt: jt, jf: jf}
	}
	prog := []ins{
		// Another architecture's calls (32-bit ones on a 64-bit kernel) have
		// other numbers: none of them is allowed.
		ld(offArch), jeq(arch, "", "eperm"),
		ld(offNr),
	}
	if runtime.GOARCH == "amd64" {
		// The x32 ABI shares the architecture value but not the numbers.
		prog = append(prog, ins{code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, k: 0x40000000, jt: "eperm"})
	}
	for _, nr := range seccompDenied {
		prog = append(prog, jeq(uint32(nr), "eperm", "")) // #nosec G115 -- system call numbers
	}
	prog = append(prog,
		// clone3 takes its flags by pointer, which a filter cannot read: say
		// it does not exist, and the C library falls back to clone.
		jeq(unix.SYS_CLONE3, "enosys", ""),
		jeq(unix.SYS_CLONE, "", "allow"),
		// Threads and processes, but no namespaces. The flags are the first argument.
		ld(offArg0),
		ins{code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, k: cloneNamespaces, jt: "eperm", jf: "allow"},
	)
	labels := map[string]int{"allow": len(prog), "eperm": len(prog) + 1, "enosys": len(prog) + 2}
	prog = append(prog, ret(allow), ret(eperm), ret(enosys))
	out := make([]byte, 0, len(prog)*8)
	for i, in := range prog {
		rel := func(label string) uint8 {
			if label == "" {
				return 0
			}
			return uint8(labels[label] - i - 1) // #nosec G115 -- the program is far shorter than 256
		}
		out = binary.NativeEndian.AppendUint16(out, in.code)
		out = append(out, rel(in.jt), rel(in.jf))
		out = binary.NativeEndian.AppendUint32(out, in.k)
	}
	return out
}

var seccompProgram = seccompFilter()

// seccompFile is the filter as a file to hand to bubblewrap, which reads it
// to its end: a pipe that holds it. nil means no filter. The read end is
// closed when the command it was made for is no longer referenced.
func seccompFile() *os.File {
	if seccompProgram == nil {
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	_, werr := w.Write(seccompProgram) // far below a pipe's capacity
	if cerr := w.Close(); werr != nil || cerr != nil {
		_ = r.Close()
		return nil
	}
	return r
}
