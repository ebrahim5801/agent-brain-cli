package store

// SetTransferTestHook installs (or clears, with nil) the between-snapshot-and-
// delta hook for external transfer tests.
func SetTransferTestHook(f func()) { testHookAfterSnapshot = f }
