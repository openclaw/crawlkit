package cache

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func makeStableDir(parent string) (string, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	// Protect the DACL from parent inheritance and give only this user access.
	// Child files inherit the same restriction from the moment they are created.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return "", err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	for range 10 {
		path := filepath.Join(parent, "crawlkit-stable-"+rand.Text())
		// Unlike os file operations, the Win32 calls below need explicit long paths.
		winPath := path
		if !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`) {
			if strings.HasPrefix(path, `\\`) {
				winPath = `\\?\UNC\` + path[2:]
			} else {
				winPath = `\\?\` + path
			}
		}
		p, err := windows.UTF16PtrFromString(winPath)
		if err != nil {
			return "", err
		}
		if err := windows.CreateDirectory(p, &sa); err != nil {
			if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return "", &os.PathError{Op: "mkdir", Path: path, Err: err}
		}
		// Filesystems without ACL support may ignore the descriptor at creation.
		actual, err := windows.GetNamedSecurityInfo(winPath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err == nil {
			var control windows.SECURITY_DESCRIPTOR_CONTROL
			control, _, err = actual.Control()
			if err == nil && control&windows.SE_DACL_PROTECTED == 0 {
				err = errors.New("temporary filesystem did not preserve the private directory ACL")
			}
		}
		if err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", &os.PathError{Op: "mkdir", Path: parent, Err: os.ErrExist}
}
