//go:build linux

package reactor

func (l *loop) Stats() LoopStats {
	return LoopStats{
		Index:              l.cfg.Index,
		Accepted:           l.c.accepted.Load(),
		Closed:             l.c.closed.Load(),
		Live:               int(l.c.live.Load()),
		BytesIn:            l.c.bytesIn.Load(),
		BytesOut:           l.c.bytesOut.Load(),
		MessagesIn:         l.c.messagesIn.Load(),
		MessagesOut:        l.c.messagesOut.Load(),
		RecvErrors:         l.c.recvErrors.Load(),
		SendErrors:         l.c.sendErrors.Load(),
		AcceptErrors:       l.c.acceptErrors.Load(),
		FileTableErrors:    l.c.fileTableErrs.Load(),
		ReadPauses:         l.c.readPauses.Load(),
		StaleCompletions:   l.c.stale.Load(),
		CQOverflow:         l.c.cqOverflow.Load(),
		BufferExhausted:    l.c.bufExhausted.Load(),
		IllegalTransitions: l.c.illegal.Load(),
		EventFDErrors:      l.c.eventFDErrors.Load(),
		EventFDErrno:       l.c.eventFDErrno.Load(),
		WakeErrors:         l.c.wakeErrors.Load(),
		TimerExpired:       l.c.timerExpired.Load(),
		Queued:             l.c.queued.Load(),
		QueuedBytes:        l.c.queuedBytes.Load(),
		Dropped:            l.c.dropped.Load(),
		Backpressured:      l.c.backpressured.Load(),
		Evicted:            l.c.evicted.Load(),
		AckTimeouts:        l.c.ackTimeouts.Load(),
		CloseSubmitted:     l.c.closeSubs.Load(),
		CloseCompleted:     l.c.closeDone.Load(),
		CloseErrors:        l.c.closeErrors.Load(),
		CloseErrno:         l.c.closeErrno.Load(),
		StaleSends:         l.c.staleSends.Load(),
		FanoutCommands:     l.c.fanoutCmds.Load(),
		FanoutRefs:         l.c.fanoutRefs.Load(),
		Submissions:        l.c.submissions.Load(),
		Completions:        l.c.completions.Load(),
	}
}
