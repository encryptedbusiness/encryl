//go:build linux

package reactor

import (
	"os"
	"testing"

	"github.com/encryptedbusiness/encryl/internal/testutil"
)

// TestMain raises the locked-memory limit before running the package tests so
// io_uring ring allocation does not fail with ENOMEM.
func TestMain(m *testing.M) {
	testutil.RaiseMemlock()
	os.Exit(m.Run())
}
