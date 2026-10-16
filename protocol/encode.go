package protocol

// AppendFramedCommand appends one varint-length-prefixed Command. It is for
// tests and client implementations; the server encodes replies and pushes.
func AppendFramedCommand(dst []byte, c *Command) []byte {
	size := commandRawSize(c)
	dst = appendVarint(dst, uint64(size))
	return appendCommandRaw(dst, c)
}

// FramedCommandSize reports the encoded size of AppendFramedCommand.
func FramedCommandSize(c *Command) int { return framedSize(commandRawSize(c)) }

// AppendFramedReply appends one varint-length-prefixed Reply. The destination
// should have room for FramedReplySize bytes.
func AppendFramedReply(dst []byte, r *Reply) []byte {
	size := replyRawSize(r)
	dst = appendVarint(dst, uint64(size))
	return appendReplyRaw(dst, r)
}

// FramedReplySize reports the encoded size of AppendFramedReply.
func FramedReplySize(r *Reply) int { return framedSize(replyRawSize(r)) }

// AppendFramedPush appends one varint-length-prefixed Reply carrying a push.
// A push always has ID zero.
func AppendFramedPush(dst []byte, p *Push) []byte {
	r := Reply{Kind: ReplyPush, Push: *p}
	return AppendFramedReply(dst, &r)
}

// FramedPushSize reports the encoded size of AppendFramedPush.
func FramedPushSize(p *Push) int {
	r := Reply{Kind: ReplyPush, Push: *p}
	return FramedReplySize(&r)
}

func appendCommandRaw(dst []byte, c *Command) []byte {
	dst = appendUint(dst, 1, uint64(c.ID))
	switch c.Kind {
	case CommandConnect:
		dst = appendNested(dst, 2, connectSize(&c.Connect))
		dst = appendConnectFields(dst, &c.Connect)
	case CommandSubscribe:
		dst = appendNested(dst, 3, subscribeSize(&c.Subscribe))
		dst = appendSubscribeFields(dst, &c.Subscribe)
	case CommandUnsubscribe:
		dst = appendNested(dst, 4, unsubscribeSize(&c.Unsubscribe))
		dst = appendUnsubscribeFields(dst, &c.Unsubscribe)
	case CommandPublish:
		dst = appendNested(dst, 5, publishSize(&c.Publish))
		dst = appendPublishFields(dst, &c.Publish)
	case CommandSend:
		dst = appendNested(dst, 6, sendSize(&c.Send))
		dst = appendSendFields(dst, &c.Send)
	case CommandAck:
		dst = appendNested(dst, 7, ackSize(&c.Ack))
		dst = appendAckFields(dst, &c.Ack)
	case CommandPong:
		dst = appendNested(dst, 8, 0)
	}
	return dst
}

func commandRawSize(c *Command) int {
	n := uintSize(1, uint64(c.ID))
	switch c.Kind {
	case CommandConnect:
		n += nestedSize(2, connectSize(&c.Connect))
	case CommandSubscribe:
		n += nestedSize(3, subscribeSize(&c.Subscribe))
	case CommandUnsubscribe:
		n += nestedSize(4, unsubscribeSize(&c.Unsubscribe))
	case CommandPublish:
		n += nestedSize(5, publishSize(&c.Publish))
	case CommandSend:
		n += nestedSize(6, sendSize(&c.Send))
	case CommandAck:
		n += nestedSize(7, ackSize(&c.Ack))
	case CommandPong:
		n += nestedSize(8, 0)
	}
	return n
}

func appendNested(dst []byte, field, size int) []byte {
	dst = appendKey(dst, field, wireBytes)
	return appendVarint(dst, uint64(size))
}

func nestedSize(field, size int) int {
	return keyLen(field, wireBytes) + varintLen(uint64(size)) + size
}

func appendConnectFields(dst []byte, c *Connect) []byte {
	dst = appendBytes(dst, 1, c.Token)
	dst = appendBytes(dst, 2, c.Client)
	dst = appendBytes(dst, 3, c.Version)
	for _, s := range c.Subscriptions {
		dst = appendBytes(dst, 4, s)
	}
	return dst
}

func connectSize(c *Connect) int {
	n := bytesSize(1, c.Token) + bytesSize(2, c.Client) + bytesSize(3, c.Version)
	for _, s := range c.Subscriptions {
		n += bytesSize(4, s)
	}
	return n
}

func appendSubscribeFields(dst []byte, s *Subscribe) []byte {
	return appendBytes(dst, 1, s.Room)
}

func subscribeSize(s *Subscribe) int { return bytesSize(1, s.Room) }

func appendUnsubscribeFields(dst []byte, u *Unsubscribe) []byte {
	return appendUint(dst, 1, uint64(u.RoomID))
}

func unsubscribeSize(u *Unsubscribe) int { return uintSize(1, uint64(u.RoomID)) }

func appendEventRefFields(dst []byte, e *EventRef) []byte {
	if e.ID != 0 {
		return appendUint(dst, 1, uint64(e.ID))
	}
	return appendBytes(dst, 2, e.Name)
}

func eventRefSize(e *EventRef) int {
	if e.ID != 0 {
		return uintSize(1, uint64(e.ID))
	}
	return bytesSize(2, e.Name)
}

func appendPublishFields(dst []byte, p *Publish) []byte {
	if p.RoomID != 0 {
		dst = appendUint(dst, 1, uint64(p.RoomID))
	} else {
		dst = appendBytes(dst, 2, p.Room)
	}
	dst = appendNested(dst, 3, eventRefSize(&p.Event))
	dst = appendEventRefFields(dst, &p.Event)
	dst = appendBytes(dst, 4, p.Payload)
	dst = appendUint(dst, 5, p.RequestID)
	return dst
}

func publishSize(p *Publish) int {
	n := 0
	if p.RoomID != 0 {
		n += uintSize(1, uint64(p.RoomID))
	} else {
		n += bytesSize(2, p.Room)
	}
	n += nestedSize(3, eventRefSize(&p.Event))
	n += bytesSize(4, p.Payload)
	n += uintSize(5, p.RequestID)
	return n
}

func appendSendFields(dst []byte, s *Send) []byte {
	dst = appendNested(dst, 1, eventRefSize(&s.Event))
	dst = appendEventRefFields(dst, &s.Event)
	dst = appendBytes(dst, 2, s.Payload)
	dst = appendUint(dst, 3, s.RequestID)
	return dst
}

func sendSize(s *Send) int {
	return nestedSize(1, eventRefSize(&s.Event)) + bytesSize(2, s.Payload) + uintSize(3, s.RequestID)
}

func appendAckFields(dst []byte, a *Ack) []byte {
	dst = appendUint(dst, 1, a.Token)
	return appendBytes(dst, 2, a.Payload)
}

func ackSize(a *Ack) int {
	return uintSize(1, a.Token) + bytesSize(2, a.Payload)
}

func appendReplyRaw(dst []byte, r *Reply) []byte {
	dst = appendUint(dst, 1, uint64(r.ID))
	if r.Error != nil {
		dst = appendNested(dst, 2, errorSize(r.Error))
		dst = appendErrorFields(dst, r.Error)
	}
	switch r.Kind {
	case ReplyConnect:
		dst = appendNested(dst, 3, connectResultSize(&r.Connect))
		dst = appendConnectResultFields(dst, &r.Connect)
	case ReplySubscribe:
		dst = appendNested(dst, 4, subscribeResultSize(&r.Subscribe))
		dst = appendSubscribeResultFields(dst, &r.Subscribe)
	case ReplyPublish:
		dst = appendNested(dst, 5, 0)
	case ReplyAck:
		dst = appendNested(dst, 6, ackResultSize(&r.Ack))
		dst = appendAckResultFields(dst, &r.Ack)
	case ReplyPush:
		dst = appendNested(dst, 7, pushSize(&r.Push))
		dst = appendPushFields(dst, &r.Push)
	}
	return dst
}

func replyRawSize(r *Reply) int {
	n := uintSize(1, uint64(r.ID))
	if r.Error != nil {
		n += nestedSize(2, errorSize(r.Error))
	}
	switch r.Kind {
	case ReplyConnect:
		n += nestedSize(3, connectResultSize(&r.Connect))
	case ReplySubscribe:
		n += nestedSize(4, subscribeResultSize(&r.Subscribe))
	case ReplyPublish:
		n += nestedSize(5, 0)
	case ReplyAck:
		n += nestedSize(6, ackResultSize(&r.Ack))
	case ReplyPush:
		n += nestedSize(7, pushSize(&r.Push))
	}
	return n
}

func appendConnectResultFields(dst []byte, c *ConnectResult) []byte {
	dst = appendBytes(dst, 1, c.ClientID)
	dst = appendUint(dst, 2, uint64(c.PingSeconds))
	if c.Pong {
		dst = appendUint(dst, 3, 1)
	}
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		dst = appendNested(dst, 4, subscribeResultSize(s))
		dst = appendSubscribeResultFields(dst, s)
	}
	return dst
}

func connectResultSize(c *ConnectResult) int {
	n := bytesSize(1, c.ClientID) + uintSize(2, uint64(c.PingSeconds))
	if c.Pong {
		n += uintSize(3, 1)
	}
	for i := range c.Subscriptions {
		n += nestedSize(4, subscribeResultSize(&c.Subscriptions[i]))
	}
	return n
}

func appendSubscribeResultFields(dst []byte, s *SubscribeResult) []byte {
	dst = appendUint(dst, 1, uint64(s.RoomID))
	return appendUint(dst, 2, uint64(s.EventID))
}

func subscribeResultSize(s *SubscribeResult) int {
	return uintSize(1, uint64(s.RoomID)) + uintSize(2, uint64(s.EventID))
}

func appendAckResultFields(dst []byte, a *AckResult) []byte {
	return appendBytes(dst, 1, a.Payload)
}

func ackResultSize(a *AckResult) int { return bytesSize(1, a.Payload) }

func appendErrorFields(dst []byte, e *Error) []byte {
	dst = appendUint(dst, 1, uint64(e.Code))
	dst = appendBytes(dst, 2, e.Message)
	if e.Temporary {
		dst = appendUint(dst, 3, 1)
	}
	return dst
}

func errorSize(e *Error) int {
	n := uintSize(1, uint64(e.Code)) + bytesSize(2, e.Message)
	if e.Temporary {
		n += uintSize(3, 1)
	}
	return n
}

func appendPushFields(dst []byte, p *Push) []byte {
	switch p.Kind {
	case PushEvent:
		dst = appendNested(dst, 1, eventPushSize(&p.Event))
		dst = appendEventPushFields(dst, &p.Event)
	case PushMessage:
		dst = appendNested(dst, 2, messageSize(&p.Message))
		dst = appendMessageFields(dst, &p.Message)
	case PushJoin:
		dst = appendNested(dst, 3, joinSize(&p.Join))
		dst = appendJoinFields(dst, &p.Join)
	case PushLeave:
		dst = appendNested(dst, 4, leaveSize(&p.Leave))
		dst = appendLeaveFields(dst, &p.Leave)
	case PushDisconnect:
		dst = appendNested(dst, 5, disconnectSize(&p.Disconnect))
		dst = appendDisconnectFields(dst, &p.Disconnect)
	case PushRequest:
		dst = appendNested(dst, 6, requestSize(&p.Request))
		dst = appendRequestFields(dst, &p.Request)
	}
	return dst
}

func pushSize(p *Push) int {
	switch p.Kind {
	case PushEvent:
		return nestedSize(1, eventPushSize(&p.Event))
	case PushMessage:
		return nestedSize(2, messageSize(&p.Message))
	case PushJoin:
		return nestedSize(3, joinSize(&p.Join))
	case PushLeave:
		return nestedSize(4, leaveSize(&p.Leave))
	case PushDisconnect:
		return nestedSize(5, disconnectSize(&p.Disconnect))
	case PushRequest:
		return nestedSize(6, requestSize(&p.Request))
	default:
		return 0
	}
}

func appendEventPushFields(dst []byte, e *EventPush) []byte {
	dst = appendUint(dst, 1, uint64(e.RoomID))
	dst = appendBytes(dst, 2, e.Room)
	dst = appendUint(dst, 3, uint64(e.EventID))
	dst = appendBytes(dst, 4, e.Payload)
	dst = appendUint(dst, 5, e.RequestID)
	dst = appendUint(dst, 6, e.Offset)
	return dst
}

func eventPushSize(e *EventPush) int {
	return uintSize(1, uint64(e.RoomID)) + bytesSize(2, e.Room) +
		uintSize(3, uint64(e.EventID)) + bytesSize(4, e.Payload) +
		uintSize(5, e.RequestID) + uintSize(6, e.Offset)
}

func appendMessageFields(dst []byte, m *Message) []byte {
	dst = appendUint(dst, 1, uint64(m.EventID))
	dst = appendBytes(dst, 2, m.Payload)
	dst = appendUint(dst, 3, m.RequestID)
	return dst
}

func messageSize(m *Message) int {
	return uintSize(1, uint64(m.EventID)) + bytesSize(2, m.Payload) + uintSize(3, m.RequestID)
}

func appendJoinFields(dst []byte, j *Join) []byte   { return appendUint(dst, 1, uint64(j.RoomID)) }
func joinSize(j *Join) int                          { return uintSize(1, uint64(j.RoomID)) }
func appendLeaveFields(dst []byte, l *Leave) []byte { return appendUint(dst, 1, uint64(l.RoomID)) }
func leaveSize(l *Leave) int                        { return uintSize(1, uint64(l.RoomID)) }

func appendDisconnectFields(dst []byte, d *Disconnect) []byte {
	dst = appendUint(dst, 1, uint64(d.Code))
	return appendBytes(dst, 2, d.Reason)
}

func disconnectSize(d *Disconnect) int {
	return uintSize(1, uint64(d.Code)) + bytesSize(2, d.Reason)
}

func appendRequestFields(dst []byte, r *Request) []byte {
	dst = appendUint(dst, 1, r.Token)
	dst = appendUint(dst, 2, uint64(r.EventID))
	return appendBytes(dst, 3, r.Payload)
}

func requestSize(r *Request) int {
	return uintSize(1, r.Token) + uintSize(2, uint64(r.EventID)) + bytesSize(3, r.Payload)
}
