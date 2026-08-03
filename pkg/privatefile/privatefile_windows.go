//go:build windows

package privatefile

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// EnsureDir creates path and every missing parent, then restricts the leaf to
// the current user. Only the leaf is hardened: the parents may legitimately be
// shared locations such as %LOCALAPPDATA%, and narrowing those would be a
// side effect no caller asked for.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return Harden(path)
}

// Harden replaces the object's DACL with a single entry granting the current
// user full access, and marks it protected so inheritable entries from the
// parent directory cannot widen it again. os.Chmod is not an alternative
// here: on Windows it only toggles the read-only attribute and grants nobody
// anything.
func Harden(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	dacl, err := ownerOnlyDACL(sid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

// Verify reports ErrNotPrivate unless every entry in the object's DACL names
// the current user. A nil DACL is rejected outright rather than treated as
// "no entries": in Windows terms it grants everyone full access, so it is the
// least private state, not the most.
func Verify(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("%w: %s has a null DACL, which grants everyone access", ErrNotPrivate, path)
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trustee.Equals(sid) {
			return fmt.Errorf("%w: %s grants access to %s", ErrNotPrivate, path, trustee)
		}
	}
	return nil
}

// OwnerOnlyAttributes returns SECURITY_ATTRIBUTES carrying the same
// owner-only DACL that Harden applies to files, for kernel objects that take
// their security at creation time rather than through SetNamedSecurityInfo —
// a named pipe being the case this exists for.
//
// It is exported only on Windows, deliberately. The alternative was to
// rebuild the SID lookup and the ACL next to the pipe code, which would have
// left two definitions of "owner-only" free to drift apart.
func OwnerOnlyAttributes() (*windows.SecurityAttributes, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	dacl, err := ownerOnlyDACL(sid)
	if err != nil {
		return nil, err
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	return &attributes, nil
}

func ownerOnlyDACL(sid *windows.SID) (*windows.ACL, error) {
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}
