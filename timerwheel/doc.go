// Package timerwheel implements the per-loop hierarchical deadline wheel.
//
// A connection acquires a node only while a deadline is armed. Nodes cascade
// from coarse buckets to the exact 10 ms bucket, so an idle connection holds no
// timer object.
//
//	w := timerwheel.New(nowMillis, 0)
//	h, _ := w.Acquire()
//	w.SetMeta(h, owner, kind)
//	w.Add(h, nowMillis, deadlineMillis)
package timerwheel
