//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported reports whether this build can apply an update at all. Only the
// Windows build ships an installer, so only it can run one.
const Supported = true

// ensureStagingDir creates dir so that only SYSTEM, Administrators and the
// account this process runs under can write to it.
//
// This is the one genuinely new risk the updater introduces. The service runs
// as LocalSystem, so a directory it executes a file from must not be one a
// standard user can write to — otherwise staging an update is a way to hand any
// local account a SYSTEM shell. ProgramData's inherited permissions are not
// obviously wrong today, but they are not something to bet elevation on, so the
// ACL is set explicitly and inheritance is switched off.
//
// The third entry, for the current process's own account, is what keeps this
// honest rather than merely strict. In production it is redundant: the service
// is LocalSystem and SYSTEM already has full control. Everywhere else — a
// developer running the engine in the foreground, the test suite — it is the
// account that has to write there, and granting it changes nothing about which
// *other* users can, which is the property that matters.
func ensureStagingDir(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		// Left as it is rather than re-secured on every pass: on an upgrade the
		// previous version created it the same way, and silently rewriting an
		// ACL an administrator may have deliberately changed is not this
		// function's decision. Re-checking the installer's hash immediately
		// before it runs is what covers the gap.
		return nil
	}

	sddl, err := stagingSDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build staging ACL: %w", err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))

	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(path, &sa); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// stagingSDDL builds the descriptor. P disables inheritance from ProgramData;
// without it, whatever ProgramData grants would apply here too. OICI makes each
// entry apply to the files inside, not just the directory.
func stagingSDDL() (string, error) {
	const base = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	// LocalSystem is already covered by the SY entry above; adding it again
	// would just be noise in the ACL an administrator reads.
	if sid == "" || sid == "S-1-5-18" {
		return base, nil
	}
	return base + "(A;OICI;FA;;;" + sid + ")", nil
}

func currentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read process account: %w", err)
	}
	return user.User.Sid.String(), nil
}

// launchInstaller starts the installer and returns without waiting for it.
//
// Not waiting is the point. setup.iss's PrepareToInstall stops LocalMonitorSvc
// before it replaces anything, so the installer kills the process that started
// it. DETACHED_PROCESS is what lets the child outlive that: the SCM does not
// kill a stopped service's descendants, and the service is not in a job object
// that would take them with it. Release() drops the handle, because nothing
// here will be alive to see the process it refers to finish.
func launchInstaller(exe, logFile string) error {
	cmd := exec.Command(exe,
		"/VERYSILENT",
		"/SUPPRESSMSGBOXES",
		"/NORESTART",
		// Nobody is watching a silent install, so this log is the only account
		// of what it did.
		"/LOG="+logFile,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
