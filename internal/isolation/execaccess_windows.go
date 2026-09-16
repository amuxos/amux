//go:build windows

package isolation

// The UID/DAC backend is Linux-only, so there is no principal whose access
// could be checked here.
func CheckExecAccessForUID(string, int, int) *ExecAccessDenial { return nil }

func CheckExecAccessForCaller(string) *ExecAccessDenial { return nil }
