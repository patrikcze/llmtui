package personalapps

// platformSupported reports whether this build can reach Apple Mail and
// Apple Calendar at all. Only darwin can: the adapters speak to macOS
// application scripting and the macOS event store.
//
// It says nothing about permissions, installed applications or a usable GUI
// session. Those are discovered by an explicit connect, never at startup.
func platformSupported() bool { return true }
