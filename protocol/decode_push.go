package protocol

func decodePush(b []byte, p *Push) error {
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
			if err := decodeEventPush(v, &p.Event); err != nil {
				return err
			}
			p.Kind = PushEvent
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeMessage(v, &p.Message); err != nil {
				return err
			}
			p.Kind = PushMessage
			i = n
		case 3:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeJoin(v, &p.Join); err != nil {
				return err
			}
			p.Kind = PushJoin
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeLeave(v, &p.Leave); err != nil {
				return err
			}
			p.Kind = PushLeave
			i = n
		case 5:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeDisconnect(v, &p.Disconnect); err != nil {
				return err
			}
			p.Kind = PushDisconnect
			i = n
		case 6:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeRequest(v, &p.Request); err != nil {
				return err
			}
			p.Kind = PushRequest
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

func decodeEventPush(b []byte, e *EventPush) error {
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
			e.RoomID = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			e.Room = v
			i = n
		case 3:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			e.EventID = uint32(v)
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			e.Payload = v
			i = n
		case 5:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			e.RequestID = v
			i = n
		case 6:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			e.Offset = v
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

func decodeMessage(b []byte, m *Message) error {
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
			m.EventID = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			m.Payload = v
			i = n
		case 3:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			m.RequestID = v
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

func decodeJoin(b []byte, j *Join) error {
	v, err := decodeRoomID(b)
	if err != nil {
		return err
	}
	j.RoomID = v
	return nil
}

func decodeLeave(b []byte, l *Leave) error {
	v, err := decodeRoomID(b)
	if err != nil {
		return err
	}
	l.RoomID = v
	return nil
}

func decodeRoomID(b []byte) (uint32, error) {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return 0, ErrWire
		}
		i = next
		if field == 1 {
			if wire != wireVarint {
				return 0, ErrWire
			}
			v, _, ok := readVarint(b, i)
			if !ok {
				return 0, ErrWire
			}
			return uint32(v), nil
		}
		n, ok := skipField(b, i, wire)
		if !ok {
			return 0, ErrWire
		}
		i = n
	}
	return 0, nil
}

func decodeDisconnect(b []byte, d *Disconnect) error {
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
			d.Code = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			d.Reason = v
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

func decodeRequest(b []byte, r *Request) error {
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
			r.Token = v
			i = n
		case 2:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			r.EventID = uint32(v)
			i = n
		case 3:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			r.Payload = v
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
