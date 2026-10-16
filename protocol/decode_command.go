package protocol

func decodeCommand(b []byte, c *Command) error {
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
			c.ID = uint32(v)
			i = n
		case 2:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeConnect(sub, &c.Connect); err != nil {
				return err
			}
			c.Kind = CommandConnect
			i = n
		case 3:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeSubscribe(sub, &c.Subscribe); err != nil {
				return err
			}
			c.Kind = CommandSubscribe
			i = n
		case 4:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeUnsubscribe(sub, &c.Unsubscribe); err != nil {
				return err
			}
			c.Kind = CommandUnsubscribe
			i = n
		case 5:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodePublish(sub, &c.Publish); err != nil {
				return err
			}
			c.Kind = CommandPublish
			i = n
		case 6:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeSend(sub, &c.Send); err != nil {
				return err
			}
			c.Kind = CommandSend
			i = n
		case 7:
			sub, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeAck(sub, &c.Ack); err != nil {
				return err
			}
			c.Kind = CommandAck
			i = n
		case 8:
			_, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			c.Kind = CommandPong
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

func decodeConnect(b []byte, c *Connect) error {
	for i := 0; i < len(b); {
		field, wire, next, ok := readKey(b, i)
		if !ok {
			return ErrWire
		}
		i = next
		switch field {
		case 1, 2, 3:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			switch field {
			case 1:
				c.Token = v
			case 2:
				c.Client = v
			case 3:
				c.Version = v
			}
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			c.Subscriptions = append(c.Subscriptions, v)
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

func decodeSubscribe(b []byte, s *Subscribe) error {
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
			s.Room = v
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

func decodeUnsubscribe(b []byte, u *Unsubscribe) error {
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
			u.RoomID = uint32(v)
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

func decodePublish(b []byte, p *Publish) error {
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
			p.RoomID = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			p.Room = v
			i = n
		case 3:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			if err := decodeEventRef(v, &p.Event); err != nil {
				return err
			}
			i = n
		case 4:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			p.Payload = v
			i = n
		case 5:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			p.RequestID = v
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

func decodeSend(b []byte, s *Send) error {
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
			if err := decodeEventRef(v, &s.Event); err != nil {
				return err
			}
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			s.Payload = v
			i = n
		case 3:
			if wire != wireVarint {
				return ErrWire
			}
			v, n, ok := readVarint(b, i)
			if !ok {
				return ErrWire
			}
			s.RequestID = v
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

func decodeEventRef(b []byte, e *EventRef) error {
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
			e.ID = uint32(v)
			i = n
		case 2:
			v, n, ok := readBytes(b, i)
			if !ok {
				return ErrWire
			}
			e.Name = v
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

func decodeAck(b []byte, a *Ack) error {
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
			a.Token = v
			i = n
		case 2:
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
