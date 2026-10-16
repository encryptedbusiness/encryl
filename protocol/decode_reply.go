package protocol

func decodeReply(b []byte, r *Reply) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			r.ID = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			var e Error
			if err := decodeError(v, &e); err != nil {
				return err
			}
			r.Error = &e
			i = n
		case 3:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeConnectResult(v, &r.Connect); err != nil {
				return err
			}
			r.Kind = ReplyConnect
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeSubscribeResult(v, &r.Subscribe); err != nil {
				return err
			}
			r.Kind = ReplySubscribe
			i = n
		case 5:
			_, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			r.Kind = ReplyPublish
			i = n
		case 6:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeAckResult(v, &r.Ack); err != nil {
				return err
			}
			r.Kind = ReplyAck
			i = n
		case 7:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodePush(v, &r.Push); err != nil {
				return err
			}
			r.Kind = ReplyPush
			i = n
		default:
			n, ok := skipField(b, i, wire)
			if !ok {
				return ErrWire
			}
			i = n
		}
	}
	return nil
}

func decodeError(b []byte, e *Error) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			e.Code = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			e.Message = v
			i = n
		case 3:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			e.Temporary = v != 0
			i = n
		default:
			n, ok := skipField(b, i, wire)
			if !ok {
				return ErrWire
			}
			i = n
		}
	}
	return nil
}

func decodeConnectResult(b []byte, c *ConnectResult) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			c.ClientID = v
			i = n
		case 2:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			c.PingSeconds = uint32(v)
			i = n
		case 3:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			c.Pong = v != 0
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			var s SubscribeResult
			if err := decodeSubscribeResult(v, &s); err != nil {
				return err
			}
			c.Subscriptions = append(c.Subscriptions, s)
			i = n
		default:
			n, ok := skipField(b, i, wire)
			if !ok {
				return ErrWire
			}
			i = n
		}
	}
	return nil
}

func decodeSubscribeResult(b []byte, s *SubscribeResult) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			s.RoomID = uint32(v)
			i = n
		case 2:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			s.EventID = uint32(v)
			i = n
		default:
			n, ok := skipField(b, i, wire)
			if !ok {
				return ErrWire
			}
			i = n
		}
	}
	return nil
}

func decodeAckResult(b []byte, a *AckResult) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			a.Payload = v
			i = n
		default:
			n, ok := skipField(b, i, wire)
			if !ok {
				return ErrWire
			}
			i = n
		}
	}
	return nil
}
