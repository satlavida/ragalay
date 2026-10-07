package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const canSetUserEnv = true

// setUserEnv stores a user environment variable (like `setx`) and tells
// running programs, so windows opened from now on see it (plan2 S11).
func setUserEnv(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(name, value); err != nil {
		return err
	}
	// WM_SETTINGCHANGE with "Environment" makes Explorer reload it.
	env, _ := windows.UTF16PtrFromString("Environment")
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	var result uintptr
	proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000,
		uintptr(unsafe.Pointer(&result)))
	return nil
}
