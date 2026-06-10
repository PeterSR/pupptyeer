//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// secureSocketDir restricts dir to the current user via an explicit,
// protected DACL. Unix mode bits (0700/0600 elsewhere) are ignored on
// Windows, so without this the local-only guarantee would rely solely on
// the parent temp dir's inherited ACL. We install a single allow-all ACE
// for the current user's SID and mark the DACL PROTECTED so inherited
// ACEs (which might grant other principals) are dropped.
//
// NOTE: cross-compiles cleanly but has NOT been exercised on real Windows
// hardware. Treat Windows support as experimental and verify before
// relying on this for isolation. It fails closed: if the ACL cannot be
// applied, the daemon refuses to start rather than run world-accessible.
func secureSocketDir(dir string) error {
	token := windows.GetCurrentProcessToken()
	tu, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("get token user: %w", err)
	}
	sid := tu.User.Sid

	ea := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}

	dacl, err := windows.ACLFromEntries(ea, nil)
	if err != nil {
		return fmt.Errorf("build DACL: %w", err)
	}

	// PROTECTED_DACL_SECURITY_INFORMATION strips inherited ACEs so only the
	// current-user ACE applies.
	if err := windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("apply DACL to %s: %w", dir, err)
	}
	return nil
}
