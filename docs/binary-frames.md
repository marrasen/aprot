# Binary Frames on the Wire

This document specifies the WebSocket binary frame aprot uses to deliver
`Blob` results, so that clients other than the generated TypeScript one — test
probes, bindings for other languages, quick tooling — can decode them.

If you are using the generated TypeScript client, you do not need any of this;
it decodes these frames already.

If you would rather not decode binary at all, you can decline it per
connection — see [Opting out](#opting-out-of-binary-frames).

## When aprot sends a binary frame

Every aprot message is a **text** frame carrying JSON, with one exception: a
value that is a top-level `aprot.Blob` is delivered over WebSocket as a
**binary** frame.

That covers three cases:

- the response to a unary call of a `Blob`-returning method,
- every server-driven refresh of a subscription to a `Blob`-returning method,
  and
- a push event whose data is a `Blob`.

"A top-level `aprot.Blob`" means `aprot.Blob`, `*aprot.Blob`, or a named type
that embeds `aprot.Blob` as its only field:

```go
type PreviewFrame struct{ aprot.Blob }
```

A push event needs that wrapper, because a push event's wire name is its Go
type name: registering `aprot.Blob` itself would produce an event called `Blob`
and allow only one per registry. Results accept the same spellings, so the rule
is one rule.

The wrapper must embed `Blob` and hold no other field, and that is checked
exactly — sole field, anonymous, type `aprot.Blob`. A struct that merely has a
`string` and a `[]byte` is never treated as a `Blob`, however closely it
resembles one; nor is a wrapper that adds a field, since the frame has nowhere
to carry it.

Everything else stays text JSON — including a `Blob` nested inside another
struct, streamed as an item, or passed as a parameter, and including a plain
`[]byte` result. Those travel as `{contentType?, data}` with base64 `data`. A
binary push therefore carries bytes and a content type and nothing else; a push
that also needs sibling fields has to travel as ordinary JSON.

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

A response (or subscription refresh):

```json
{ "version": 1, "type": "response", "id": "42", "contentType": "image/png" }
```

A push:

```json
{ "version": 1, "type": "push", "event": "PreviewFrame", "contentType": "image/jpeg" }
```

| Field         | Type   | Notes                                                        |
| ------------- | ------ | ------------------------------------------------------------ |
| `version`     | number | Frame format version. Currently always `1`.                   |
| `type`        | string | `"response"` or `"push"`.                                     |
| `id`          | string | On a `"response"`, correlates with the `id` of the originating request, or with the subscription id for a refresh — the same id space as text `response` frames. **Omitted on a `"push"`**, which has no request to correlate with. |
| `event`       | string | On a `"push"`, the event name, matching the `event` field of a text `push` frame. Omitted on a `"response"`. |
| `contentType` | string | Omitted when the server left `Blob.ContentType` empty.        |

Treat a frame whose `version` is not `1`, or whose `type` is neither
`"response"` nor `"push"`, as an error. When such a frame carries an `id`,
**fail the pending request for that `id`** rather than ignoring it — see the
pitfall below. A frame with no `id` has no caller to fail, so there is nothing
to do but ignore it.

A client written against an earlier version of this document will not receive a
`"push"` frame unless the server it talks to pushes a `Blob`, which a server
only does once someone registers a push event with a `Blob` data type.

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
  if (header.type === 'push') {
    dispatchPush(header.event, { contentType: header.contentType, data: payload });
    return;
  }
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

Three defenses:

- Handle binary frames, per the decoder above.
- Or decline them with `?binary=0`, and check the `config` frame to confirm.
- Reject unknown frames instead of ignoring them. When the header carries an
  `id`, a client that cannot make sense of a frame can still fail the
  corresponding pending request with a clear error rather than leaving the
  caller to hang. A `"push"` frame carries no `id`; dropping one loses an
  event, which is a visible bug but never a hang.

## Opting out of binary frames

A WebSocket client can decline binary frames for the lifetime of a connection
by passing `binary=0` on the upgrade URL:

```js
const ws = new WebSocket('wss://example.com/ws?binary=0');
```

`Blob` results then arrive as ordinary text frames carrying the JSON `$blob`
envelope below, exactly as they do on the byte-stream transport. Nothing else about
the connection changes. Accepted values are `1`/`true`/`yes`/`on` and
`0`/`false`/`no`/`off`, case-insensitive; omitting the parameter means binary
frames. **An unrecognized value fails the upgrade with `400 Bad Request`** —
a typo that quietly re-enabled binary frames would reinstate the silent hang
this parameter exists to prevent.

The cost is base64: the payload inflates by about a third and both ends pay to
encode and decode it. If you are moving images or files of any size, implement
the decoder above instead.

### Confirming what you negotiated

The `config` frame the server sends immediately after the upgrade — before any
request can be made — reports the mode in effect:

```json
{ "type": "config", "binaryFrames": false }
```

Read it rather than assuming. `binaryFrames` is always present on a server that
supports negotiation, so a missing field means an older server, i.e. binary
frames on WebSocket. On the byte-stream transport it is always `false`.

This is the one signal available before a `Blob` response can hang you: a
client that cannot decode binary frames should check `binaryFrames` at connect
time and fail loudly if it is `true`. aprot cannot detect a dropped frame, so
there is no server-side equivalent.

## JSON fallback

Transports without a native binary channel (byte-stream) — and WebSocket
connections that passed `binary=0` — deliver the same value as an ordinary
text `response` frame whose result is a one-key envelope:

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
