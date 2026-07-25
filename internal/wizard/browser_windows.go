package wizard

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openBrowser launches url in the currently logged-on user's interactive
// session. The wizard runs inside the Windows service process — SYSTEM
// context, Session 0 — and Windows has isolated services from the
// interactive desktop since Vista, so a plain child-process launch from
// here either fails outright or opens invisibly in Session 0 where nobody
// can see it (confirmed directly on a real Windows VM). This uses the
// standard mechanism a service uses to reach across that boundary: find
// the active session (WTSEnumerateSessions), borrow that session's user
// token (WTSQueryUserToken — requires SeTcbPrivilege, which LocalSystem
// holds by default), and launch the process under that token
// (CreateProcessAsUser) so it lands on the user's desktop instead of
// Session 0's.
func openBrowser(url string) error {
	sessionID, err := activeSessionID()
	if err != nil {
		return fmt.Errorf("find active session: %w", err)
	}

	var userToken windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &userToken); err != nil {
		return fmt.Errorf("query user token: %w", err)
	}
	defer userToken.Close()

	var primaryToken windows.Token
	if err := windows.DuplicateTokenEx(userToken, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primaryToken); err != nil {
		return fmt.Errorf("duplicate token: %w", err)
	}
	defer primaryToken.Close()

	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, primaryToken, false); err != nil {
		return fmt.Errorf("create environment block: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)

	// rundll32 url.dll,FileProtocolHandler is the same mechanism the
	// original (Session-0-only) implementation used — it opens url in the
	// user's default browser without flashing a console window, unlike
	// "cmd /c start".
	cmdLine, err := windows.UTF16PtrFromString(fmt.Sprintf(`rundll32.exe url.dll,FileProtocolHandler %s`, url))
	if err != nil {
		return err
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return err
	}

	si := &windows.StartupInfo{Desktop: desktop}
	pi := &windows.ProcessInformation{}
	if err := windows.CreateProcessAsUser(
		primaryToken,
		nil,
		cmdLine,
		nil,
		nil,
		false,
		windows.CREATE_UNICODE_ENVIRONMENT,
		env,
		nil,
		si,
		pi,
	); err != nil {
		return fmt.Errorf("create process as user: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	return nil
}

// activeSessionID finds the session ID of the currently logged-on
// interactive user. WTSGetActiveConsoleSessionId only covers the physical
// console; enumerating sessions and matching WTSActive also finds an RDP
// session — the case that matters for provisioning a remote/virtual
// machine rather than standing at physical hardware (exactly how this was
// tested).
func activeSessionID() (uint32, error) {
	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &count); err != nil {
		return 0, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	list := unsafe.Slice(sessions, count)
	for _, s := range list {
		if s.State == windows.WTSActive {
			return s.SessionID, nil
		}
	}
	return 0, fmt.Errorf("no active interactive session found")
}
