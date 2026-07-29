# Binary Frames on the Wire

This document specifies the WebSocket binary frame aprot uses to deliver
`Blob` results, so that clients other than the generated TypeScript one — test
probes, bindings for other languages, quick tooling — can decode them.

If you are using the generated TypeScript client, you do not need any of this;
it decodes these frames already.

## When aprot sends a binary frame

Every aprot message is a **text** frame carrying JSON, with exactly one
exception: a handler whose top-level result is `aprot.Blob` (or `*aprot.Blob`)
is delivered over WebSocket as a **binary** frame.

That covers two cases:

- the response to a unary call of a `Blob`-returning method, and
- every server-driven refresh of a subscription to a `Blob`-returning method.

Everything else stays text JSON — including a `Blob` nested inside another
struct, streamed as an item, or passed as a parameter, and including a plain
`[]byte` result. Those travel as `{contentType?, data}` with base64 `data`.

A `nil` `*Blob` result is not a blob: it takes the ordinary JSON path and
arrives as a `null` result, like any other nil pointer.

## Frame layout

```
┌────────────┬──────────────────────┬─────────────────────────┐
│  4 bytes   │     headerLen bytes  │    rest of the frame    │
│ headerLen  │     JSON header      │     raw payload bytes   │
│ big-endian │                      │  (no base64, no framing)│
└────────────┴──────────────────────┴─────────────────────────┘
```

- **`headerLen`** — unsigned 32-bit, **big-endian** (network byte order). The
  byte length of the JSON header that follows.
- **JSON header** — UTF-8 JSON object, see below.
- **payload** — the raw `Blob.Data` bytes, from offset `4 + headerLen` to the
  end of the frame. The payload length is implied by the frame length; it is
  not encoded in the header.

## Header fields

```json
{ "version": 1, "type": "response", "id": "42", "contentType": "image/png" }
```

| Field         | Type   | Notes                                                        |
| ------------- | ------ | ------------------------------------------------------------ |
| `version`     | number | Frame format version. Currently always `1`.                   |
| `type`        | string | Currently always `"response"`.                                |
| `id`          | string | Correlates with the `id` of the originating request, or with the subscription id for a refresh. Same id space as text `response` frames. |
| `contentType` | string | Omitted when the handler left `Blob.ContentType` empty.       |

Clients should treat a frame whose `version` is not `1`, or whose `type` is not
`"response"`, as an error and **fail the pending request for that `id`** rather
than ignoring the frame — see the pitfall below. The header is guaranteed to
carry an `id` even for otherwise unrecognized frames.

## Reference decoder

JavaScript / TypeScript (browser `WebSocket` or `ws` in Node):

```js
// ws.binaryType = 'arraybuffer';  // browsers default to 'blob'
function decodeBinaryFrame(buffer) {
  const view = new DataView(buffer);
  const headerLen = view.getUint32(0, false); // false = big-endian
  const header = JSON.parse(
    new TextDecoder().decode(new Uint8Array(buffer, 4, headerLen)),
  );
  const payload = new Uint8Array(buffer, 4 + headerLen);
  return { header, payload };
}
```

Wiring it into a message handler that already parses text frames:

```js
ws.onmessage = (ev) => {
  if (typeof ev.data === 'string') {
    handleJSON(JSON.parse(ev.data));
    return;
  }
  const { header, payload } = decodeBinaryFrame(ev.data);
  settle(header.id, { contentType: header.contentType, data: payload });
};
```

Python (`websockets`), where a binary frame arrives as `bytes`:

```python
import json, struct

def decode_binary_frame(frame: bytes):
    (header_len,) = struct.unpack_from(">I", frame, 0)
    header = json.loads(frame[4 : 4 + header_len])
    return header, frame[4 + header_len :]
```

## Common pitfall: the silent hang

A client that only handles text frames drops `Blob` responses on the floor. The
server considers the request complete — the handler returned, the frame was
written — so there is no error, no timeout, and no server-side trace. The
pending request simply never settles, which looks exactly like a server
deadlock. For a *subscription* to a `Blob` method, there isn't even a hang: the
refreshes silently never arrive and the client shows stale data forever.

If a single RPC hangs while every other call on the same connection works,
check whether that method returns a `Blob`.

Two defenses:

- Handle binary frames, per the decoder above.
- Reject unknown frames instead of ignoring them. Since the header always
  carries an `id`, a client that cannot make sense of a frame can still fail
  the corresponding pending request with a clear error rather than leaving the
  caller to hang.

## JSON fallback

Transports without a native binary channel (SSE, byte-stream) deliver the same
value as an ordinary text `response` frame whose result is a one-key envelope:

```json
{
  "type": "response",
  "id": "42",
  "result": { "$blob": { "contentType": "image/png", "data": "iVBORw0KG..." } }
}
```

`data` is standard base64. A client that implements this shape as well can
reconstruct an identical value on every transport — this is exactly what the
generated TypeScript client does, which is why its resolved type does not
depend on the transport.
