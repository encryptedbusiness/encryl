# Encryl — A Visual Guide

This guide explains the v2 server in simple English. It also connects the new
design to the old `uring-io-rewrite` branch. Read this guide if you knew the
old branch and you want to know what changed and why.

The guide uses short sentences. It uses the active voice. It defines each term
one time. The diagrams show the important ideas.

## 1. What the server does

The server keeps many WebSocket connections open. A client sends an event. The
server finds the handler for that event. The handler can send events to one
connection, to a room, or to all connections.

```mermaid
flowchart LR
    C1[Client A] <--> S[Encryl server]
    C2[Client B] <--> S
    C3[Client C] <--> S
    S --- R[(Room: chat)]
    S --- Q[(Room: alerts)]
```

## 2. The hard problem

Most connections are idle at any moment. A chat room can have one million
members. Perhaps ten members speak each second. The other 999,990 members only
listen.

The server must hold one million connections in a small memory space. The
server must not reserve a thread, a stack, or a big buffer for each idle
connection.

```mermaid
flowchart TD
    M[1,000,000 open connections] --> A[10 connections send data]
    M --> I[999,990 connections are idle]
    I --> Z[An idle connection must hold no buffer]
    A --> W[An active connection uses a small queue]
```

## 3. The design you knew

The old branch had good ideas. A subscriber had a file descriptor, an ID, and a
list of rooms. A room held a map of subscribers. A broadcast walked the map and
wrote to each subscriber. Each subscriber had its own goroutine for reading.

```mermaid
flowchart TD
    subgraph old[Old branch: uring-io-rewrite]
        H[Hub] --> R1[Room: chat]
        R1 --> S1[Subscriber<br/>fd 41<br/>buffer<br/>goroutine]
        R1 --> S2[Subscriber<br/>fd 42<br/>buffer<br/>goroutine]
        S1 --> E1[Compile 1 frame per receiver]
        S2 --> E2[Compile 1 frame per receiver]
    end
```

The old branch had these costs:

| Old part | Cost for each connection |
| -------- | ------------------------ |
| `Subscriber.Capture` goroutine | About 8 KiB of stack |
| Per-subscriber buffer | Up to the buffer size |
| Room key by file descriptor | The kernel can reuse the number |
| One frame copy for each receiver | One allocation for each receiver |
| One callback per I/O operation | One more function call on each step |

The old branch also sent text frames. The new server sends binary frames only.

## 4. The new design in one picture

The server has three layers. The facade is the public API. The slices own the
features. The kernel owns the Linux primitives. A dependency points down. A
lower layer never uses a higher layer.

```mermaid
flowchart TD
    subgraph facade[Facade package encryl]
        API[Server, Conn, Emitter<br/>On, Emit, ACK]
    end
    subgraph slices[Vertical slices]
        EV[event<br/>wire format and handlers]
        RM[room<br/>rooms and fan-out]
        CN[connection<br/>slot, state, queue]
        TR[transport<br/>HTTP and WebSocket parsers]
    end
    subgraph kernel[Shared kernel]
        RE[reactor<br/>one loop per core]
        PL[platform<br/>io_uring and buffers]
        TW[timerwheel<br/>deadlines]
    end
    API --> EV
    API --> RM
    API --> CN
    EV --> CN
    RM --> CN
    TR --> RE
    CN --> RE
    CN --> PL
    RE --> PL
    RE --> TW
```

## 5. Idea 1: One loop owns many connections

A loop is one OS thread with one io_uring ring. The loop owns a fixed set of
connections. All work for a connection runs on its own loop. No goroutine
exists for each connection.

The old model started a new goroutine after each read. The new model keeps one
goroutine for the whole loop.

```mermaid
flowchart LR
    subgraph old[Old model]
        G1[goroutine 1] --> F1[connection 1]
        G2[goroutine 2] --> F2[connection 2]
        G3[goroutine 3] --> F3[connection 3]
    end
    subgraph new[New model]
        L1[Loop 1] --> N1[connection 1]
        L1 --> N2[connection 2]
        L1 --> N3[connection 3]
        L1 --> N4[10,000 more]
    end
```

One loop on one core runs one handler at a time. For this reason, a handler
must not block. A blocking handler stops every connection on that core.

## 6. Idea 2: A connection has a safe name

The old code used the file descriptor as the identity. The kernel can close a
descriptor and give the same number to a new socket. A late message can then go
to the wrong connection.

The new code uses two numbers. The slot is the position in a table. The
generation increases each time the server reuses the slot. A message for an old
connection fails the generation check.

```mermaid
flowchart TD
    ID[ConnID<br/>slot 7, generation 3] --> T[Connection table]
    T --> S7[slot 7<br/>generation 3<br/>live]
    OLD[Late message<br/>slot 7, generation 2] --> T
    T -.->|generation does not match| DROP[Drop the message]
```

The public handle is small: a server pointer, a slot, and a generation. The
server checks the pair on every call.

## 7. Idea 3: Every send goes through a room

A room is a named group of connections. A Self-Room is the private room of one
connection. The Self-Room is implicit. The server does not store a member list
for it. The Self-Room costs zero bytes.

This is your old idea. Your branch called `JoinRoom(id, s)` with the session ID.
The new server makes the Self-Room a first-class target.

```mermaid
flowchart TD
    subgraph named[Stored rooms]
        A[Room: chat]
        B[Room: alerts]
    end
    subgraph self[Implicit Self-Rooms]
        SA[Self-Room of A]
        SB[Self-Room of B]
        SC[Self-Room of C]
    end
    CA[Connection A] --- SA
    CB[Connection B] --- SB
    CC[Connection C] --- SC
    CA --- A
    CB --- A
    CB --- B
    CC --- B
```

A unicast send is an Emit to one Self-Room. A room send is an Emit to one named
room. A broadcast is an Emit to all open connections. All three paths use one
engine.

## 8. Idea 4: Encode one time. Share the frame.

A fan-out sends the same bytes to many connections. The server encodes the
frame one time. The frame has a reference count. Each loop takes one reference.
The frame returns to the pool when the last loop releases it.

Each connection also has a write queue. The queue is bounded by messages and by
bytes. A full queue returns `ErrBackpressure`. The server does not block and
does not drop without a signal.

```mermaid
flowchart LR
    CALL[Emit to room] --> ENC[Encode one frame]
    ENC --> REF[Frame with refcount]
    REF --> L1[Loop 1 queue]
    REF --> L2[Loop 2 queue]
    REF --> L3[Loop 3 queue]
    L1 --> C1[connections on loop 1]
    L2 --> C2[connections on loop 2]
    L3 --> C3[connections on loop 3]
```

```mermaid
flowchart TD
    SEND[Send request] --> Q{Queue has room?}
    Q -->|yes| ADD[Add frame to queue]
    Q -->|no, reject policy| ERR[Return ErrBackpressure]
    Q -->|no, drop policy| DROP[Drop the frame and count it]
    Q -->|no, slow policy| CLOSE[Close the slow connection]
    ADD --> SUB[Submit a send to io_uring]
```

## 9. Idea 5: Ask and answer with an ACK

A request can ask for an answer. The request carries a request ID. The peer
sends an ACK with the same ID. The server calls your callback one time: on the
ACK, on a timeout, or on an error.

The server binds the request to the connection that made it. A different
connection cannot answer it with a guessed ID. The timeout uses the loop timer
wheel. The server does not create a timer for each request.

```mermaid
sequenceDiagram
    participant App as Your handler
    participant S as Server
    participant P as Peer
    App->>S: Ask(event, payload)
    S->>P: EVENT with request ID 42
    P-->>S: ACK with request ID 42
    S-->>App: callback with the answer
    Note over S,App: No ACK before the deadline gives ErrAckTimeout
```

## 10. The journey of one message

```mermaid
sequenceDiagram
    participant P as Peer
    participant K as Linux kernel
    participant L as Loop
    participant W as WebSocket parser
    participant E as Event handler
    participant R as Room engine
    P->>K: binary WebSocket frame
    K->>L: read completion
    L->>W: bytes from the read buffer
    W->>E: event ID and payload
    E->>R: Emit to a room
    R->>L: one frame per target loop
    L->>K: send queue and send operation
    K->>P: binary WebSocket frame
```

The payload is borrowed. It is valid only during the handler call. The server
returns the read buffer to the pool after the handler returns.

## 11. Old words and new words

| Old branch | New server | What it means |
| ---------- | ---------- | ------------- |
| `Subscriber` | `Conn` | The handle for one connection |
| `fd` | `ConnID{Slot, Gen}` | The safe name of a connection |
| `StartUp(n)` | `New(Config)` | Create the server |
| `On["chat"]` | `On(1024, handler)` | Bind a handler to a numeric event ID |
| `sub.Send(data)` | `c.Emit(event, data)` | Send to this connection |
| `EmitDirect(sub, event, data)` | `c.Emit(event, data)` | The same, through the Self-Room |
| `Emit(event, data, "room")` | `srv.To("room").Emit(event, data)` | Send to a room |
| `Broadcast(event, data)` | `srv.Broadcast(event, data)` | Send to all connections |
| `AddToRoom(sub, name)` | `c.Join(name)` | Join a room |
| `RemoveFromRoom(sub, name)` | `c.Leave(name)` | Leave a room |
| `GetRoomMembers(name)` | `srv.RoomSize(name)` | Count the members |
| `sub.Metadata` | `Conn` fields and rooms | Per-connection data |
| One goroutine per `Capture` | One loop per core | Who waits for the network |
| Text frames | Binary frames | The wire format |
| One frame per receiver | One shared frame | Fan-out cost |
| Room map keyed by fd | Sharded directory keyed by `Ref` | Room storage |
| `xsync.Map` | Sharded maps with locks | Concurrency style |

## 12. Your ideas that survived

The rewrite does not reject your design. It keeps the important ideas and
removes the costs.

```mermaid
flowchart LR
    subgraph old[Your branch]
        I1[Self-Room idea]
        I2[Room by name]
        I3[Session ID per connection]
        I4[io_uring]
        I5[Buffer pool]
    end
    subgraph new[New server]
        N1[Zero-cost Self-Room]
        N2[Sharded room directory]
        N3[Slot plus generation]
        N4[io_uring only]
        N5[Per-loop fixed arenas]
    end
    I1 --> N1
    I2 --> N2
    I3 --> N3
    I4 --> N4
    I5 --> N5
```

The server also changes three costs: no goroutine for each connection, no
buffer for each idle connection, and no frame copy for each receiver.

## 13. How to read the code

```mermaid
flowchart TD
    A[encryl.go<br/>package documentation] --> B[server.go<br/>New and lifecycle]
    B --> C[dispatch.go<br/>open, close, message]
    B --> D[stats.go<br/>counters]
    C --> E[events.go<br/>On, Use, UseConnect]
    C --> F[emitter.go<br/>To, ToConns, Broadcast]
    F --> G[room<br/>directory, Self-Room, planner]
    C --> H[event<br/>envelope, registry, ACK]
    G --> I[connslab<br/>slot, state, write queue]
    H --> I
    I --> J[reactor<br/>loop and io_uring]
    J --> K[uring/netio<br/>ring, tags, probe, buffers]
    J --> L[timerwheel<br/>deadlines]
    I --> M[ws / httpx<br/>HTTP upgrade and WebSocket parser]
```

Read the code in this order:

1. `docs/protocol.md` for the 16-byte envelope.
2. `connslab/slab.go` for the slot and the generation.
3. `room/directory.go` and `selfroom.go` for rooms.
4. `connslab/writeq.go` for the bounded queue.
5. `reactor/loop_linux.go` and the other `*_linux.go` files for the
   loop.

## 14. Rules you must obey

- A handler must not block. It runs on a loop thread. A blocked handler stops
  every connection on that core.
- A payload is borrowed. Do not keep it after the handler returns. Copy it
  first if you need it later.
- Check the `Emit` error. A full queue returns `ErrBackpressure` under the
  default policy.
- Use the Self-Room for a unicast send. Use a named room for a group send.
- Do not use a stale `ConnID`. The generation check protects the server, but a
  stale ID cannot reach a live connection.

## 15. Small glossary

| Term | Meaning |
| ---- | ------- |
| **ACK** | The envelope that confirms a request |
| **Envelope** | The first 16 bytes of a message |
| **Event ID** | The number that selects a handler |
| **Facade** | The public package that users import |
| **Frame** | One encoded WebSocket message |
| **Generation** | The number that makes a reused slot safe |
| **Loop** | One thread with one io_uring ring |
| **Ring** | The io_uring submission and completion queues |
| **Self-Room** | The implicit room of one connection |
| **Slice** | One package that owns one feature |
| **Slot** | The position of a connection in the table |
| **Write queue** | The frames that wait for the socket |

## 16. Measured results

These numbers come from the M2 report. The host is an 8 vCPU, 15.6 GiB Linux
virtual machine. The kernel is 6.8. The Go version is 1.27.2.

| Test | Result |
| ---- | ------ |
| 1,000,000 idle connections | 1,000,000 open, zero errors |
| Memory for each idle connection | 0.392 KiB |
| 1M echo run | 1,886,007 in, 1,886,007 out |
| Echo allocation | 0 bytes per operation |
| 8k Self-Room echo | 546,374 messages per second |
| Struct size: slot | 160 bytes |

The 60-minute hold and the 1M-member room fan-out are still open. See
`docs/benchmarks/` for the raw data and the commands.
