//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func isCrossDevice(err error) bool {
	// MoveFile across volumes fails with ERROR_NOT_SAME_DEVICE.
	return errors.Is(err, syscall.Errno(17))
}

func isReadOnlyFS(err error) bool {
	return errors.Is(err, syscall.Errno(19)) // ERROR_WRITE_PROTECT
}

func elevatedHint() string {
	return "start an Administrator PowerShell or Command Prompt, then: llmtui self install --system"
}

func binDirOnPath(dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p != "" && strings.EqualFold(filepath.Clean(p), dir) {
			return true
		}
	}
	return false
}

// updateSystemPath adds dir to the persistent PATH: the machine environment
// (HKLM) for a system install, the user environment (HKCU) otherwise. Only
// the Path value is touched; all existing entries are preserved and no
// duplicate is added. Running shells keep their old PATH until reopened.
func updateSystemPath(dir string, scope Scope) (changed bool, note string, err error) {
	var key registry.Key
	var path string
	if scope == ScopeSystem {
		key, err = registry.OpenKey(registry.LOCAL_MACHINE,
			`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
		path = "machine"
	} else {
		key, err = registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
		path = "user"
	}
	if err != nil {
		return false, "", fmt.Errorf("open %s environment registry key: %w", path, err)
	}
	defer key.Close()

	current, valType, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, "", fmt.Errorf("read %s PATH: %w", path, err)
	}
	if valType == 0 {
		valType = registry.EXPAND_SZ
	}

	merged, changed := mergePathEntry(current, dir, string(os.PathListSeparator), true)
	if !changed {
		return false, "", nil
	}
	if valType == registry.EXPAND_SZ {
		err = key.SetExpandStringValue("Path", merged)
	} else {
		err = key.SetStringValue("Path", merged)
	}
	if err != nil {
		return false, "", fmt.Errorf("update %s PATH: %w", path, err)
	}
	broadcastEnvironmentChange()
	return true, fmt.Sprintf("added %s to the %s PATH; reopen terminals to pick it up", dir, path), nil
}

// broadcastEnvironmentChange tells running shells that the environment block
// changed. Best effort — failure is not fatal.
func broadcastEnvironmentChange() {
	env, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	_, _, _ = proc.Call(uintptr(hwndBroadcast), uintptr(wmSettingChange), 0,
		uintptr(unsafe.Pointer(env)), uintptr(smtoAbortIfHung), uintptr(5000), 0)
}
