(() => {
  "use strict";

  const VERSION = 2;
  const KIND_EVENT = 2;
  const KIND_ACK = 3;
  const KIND_ERROR = 4;
  const WS_URL = window.ENCRYL_WS;
  const PROTOCOL = "encryl.v2";

  const messages = document.getElementById("messages");
  const status = document.getElementById("status");
  const form = document.getElementById("form");
  const roomInput = document.getElementById("room");
  const textInput = document.getElementById("text");
  const sendButton = document.getElementById("send");
  const askButton = document.getElementById("ask");

  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const handlers = new Map();
  const pending = new Map();
  let nextID = 1;
  let ws = null;
  let connected = false;

  // FNV-1a 32 over UTF-8 bytes. This is the public event ID contract.
  function fnv32(name) {
    let h = 0x811c9dc5;
    for (const b of encoder.encode(name)) {
      h ^= b;
      h = Math.imul(h, 0x01000193) >>> 0;
    }
    return h >>> 0;
  }

  // Native v2 envelope: 16 bytes, big endian, then the JSON payload bytes.
  function encode(kind, event, request, data) {
    const body = data instanceof Uint8Array
      ? data
      : encoder.encode(JSON.stringify(data ?? {}));
    const buf = new ArrayBuffer(16 + body.length);
    const view = new DataView(buf);
    view.setUint8(0, VERSION);
    view.setUint8(1, kind);
    view.setUint16(2, 0, false);
    view.setUint32(4, event >>> 0, false);
    const req = typeof request === "bigint" ? request : BigInt(request ?? 0);
    view.setBigUint64(8, req, false);
    new Uint8Array(buf, 16).set(body);
    return buf;
  }

  function send(kind, event, request, data) {
    if (!connected || !ws || ws.readyState !== WebSocket.OPEN) {
      throw new Error("not connected");
    }
    ws.send(encode(kind, event, request, data));
  }

  function emit(name, data) {
    send(KIND_EVENT, fnv32(name), 0n, data);
  }

  function request(name, data, callback, timeoutMs = 5000) {
    const id = BigInt(nextID++);
    const key = id.toString();
    const timer = setTimeout(() => {
      if (pending.delete(key)) callback(new Error("request timed out"));
    }, timeoutMs);
    pending.set(key, (err, payload) => {
      clearTimeout(timer);
      callback(err, payload);
    });
    try {
      send(KIND_EVENT, fnv32(name), id, data);
    } catch (err) {
      clearTimeout(timer);
      pending.delete(key);
      throw err;
    }
  }

  function on(name, callback) {
    handlers.set(fnv32(name), callback);
  }

  function parse(payload) {
    try {
      return JSON.parse(decoder.decode(payload));
    } catch {
      return null;
    }
  }

  function append(who, text) {
    const row = document.createElement("div");
    row.className = "msg";
    if (who) {
      const label = document.createElement("span");
      label.className = "who";
      label.textContent = who + " · ";
      row.appendChild(label);
    }
    row.appendChild(document.createTextNode(text));
    messages.appendChild(row);
    messages.scrollTop = messages.scrollHeight;
  }

  function handleError(payload) {
    let text = "server error";
    if (payload.length >= 2) {
      const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
      const code = view.getUint16(0, false);
      text = `server error ${code}: ${decoder.decode(payload.subarray(2))}`;
    }
    status.textContent = text;
  }

  function handleFrame(buf) {
    if (buf.byteLength < 16) return;
    const view = new DataView(buf);
    if (view.getUint8(0) !== VERSION) return;
    const kind = view.getUint8(1);
    const event = view.getUint32(4, false);
    const req = view.getBigUint64(8, false);
    const payload = new Uint8Array(buf, 16);

    if (kind === KIND_EVENT) {
      const callback = handlers.get(event);
      if (callback) callback(payload);
      return;
    }
    if (kind === KIND_ACK) {
      const key = req.toString();
      const callback = pending.get(key);
      if (callback) {
        pending.delete(key);
        callback(null, payload);
      }
      return;
    }
    if (kind === KIND_ERROR) {
      handleError(payload);
    }
  }

  function closePending(reason) {
    for (const callback of pending.values()) {
      callback(new Error(reason));
    }
    pending.clear();
  }

  function connect() {
    status.textContent = "connecting…";
    ws = new WebSocket(WS_URL, [PROTOCOL]);
    ws.binaryType = "arraybuffer";

    ws.onopen = () => {
      connected = true;
      status.textContent = "connected · " + WS_URL;
      sendButton.disabled = false;
      askButton.disabled = false;
      try {
        emit("chat.join", { room: roomInput.value.trim() || "lobby" });
      } catch (err) {
        append("error", err.message);
      }
    };

    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) handleFrame(event.data);
    };

    ws.onerror = () => {
      status.textContent = "connection error";
    };

    ws.onclose = () => {
      connected = false;
      sendButton.disabled = true;
      askButton.disabled = true;
      closePending("disconnected");
      status.textContent = "disconnected; retrying in 1.5 s…";
      setTimeout(connect, 1500);
    };
  }

  on("chat.say", (payload) => {
    const msg = parse(payload);
    if (msg) append(msg.room || "room", msg.text || "");
  });

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const text = textInput.value.trim();
    if (!text) return;
    const room = roomInput.value.trim() || "lobby";
    try {
      emit("chat.say", { room, text });
      append("you", text);
      textInput.value = "";
    } catch (err) {
      append("error", err.message);
    }
  });

  askButton.addEventListener("click", () => {
    const question = textInput.value.trim() || "hello?";
    try {
      request("chat.ask", { question }, (err, payload) => {
        if (err) {
          append("server", err.message);
          return;
        }
        const reply = parse(payload);
        if (reply) {
          append("server", reply.question + " @ " + reply.at);
        }
      });
    } catch (err) {
      append("error", err.message);
    }
  });

  roomInput.addEventListener("change", () => {
    try {
      emit("chat.join", { room: roomInput.value.trim() || "lobby" });
    } catch {
      // The join is also sent on reconnect.
    }
  });

  connect();
})();
