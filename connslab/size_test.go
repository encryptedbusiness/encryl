package connslab

import (
	"testing"
	"unsafe"
)

func TestStructSizes(t *testing.T) {
	slot := unsafe.Sizeof(Slot{})
	acct := unsafe.Sizeof(Accounting{})
	frame := unsafe.Sizeof(Frame{})
	node := unsafe.Sizeof(Node{})
	t.Logf("Slot=%d Accounting=%d Frame=%d Node=%d", slot, acct, frame, node)
	if slot > 160 {
		t.Fatalf("Slot size = %d, want <= 160 bytes", slot)
	}
}
