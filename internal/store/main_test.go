package store

import (
	"os"
	"testing"
)

// TestMain keeps new test stores in the pre-V2 format these tests were written
// for; tests that need Memory V2 upgrade explicitly, and the V2 default has its
// own tests that clear this variable.
func TestMain(m *testing.M) {
	if err := os.Setenv("AGENT_BRAIN_MEMORY_V2_DEFAULT", "off"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
