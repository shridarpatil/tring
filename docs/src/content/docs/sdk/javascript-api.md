---
title: JavaScript API
description: The programmatic browser SDK. Covers Tring (agents), TringGuest (visitors), Call, the low-level RtcCall and RtcAgent clients, and their events.
---

Use the JavaScript API when the drop-in widgets don't fit, for example when you build your own call UI or need call events in your app. Everything here comes from `sdk/js/src`.

| Export | For |
|---|---|
| [`Tring`](#tring) | **Agents.** Connects to the gateway, keeps the agent online, and gives you a [`Call`](#call) to answer, control and hang up. It can't place calls. |
| [`TringGuest`](#tringguest) | **Visitors.** Places one call into an IVR flow, using credentials from your backend. |
| [`RtcCall`](#rtccall), [`RtcAgent`](#rtcagent) | The low-level clients for the gateway's `/rtc` protocol that the two classes above are built on. |
| `ClientMsgType`, `ServerMsgType` | The protocol's message type names (see [The /rtc protocol](#the-rtc-protocol)). |

## Loading the SDK

**Bundler (ESM).** Build `sdk/js` and import from `@tring/voice`:

```js
import Tring, { TringGuest, RtcCall, RtcAgent } from "@tring/voice";
```

`Tring` is both the default export and a named export.

**Script tag.** The API serves the bundles, and each sets one global:

| Bundle | Global |
|---|---|
| `/sdk/tring.js` | `window.Tring` (the `Tring` class only) |
| `/sdk/agent.js` | `window.Tring`, and it also starts the [agent widget](/tring/sdk/agent-widget/) |
| `/sdk/voice.js` | `window.TringGuest`, and it also starts the [visitor widget](/tring/sdk/visitor-widget/) if its `data-*` attributes are set |

`RtcCall`, `RtcAgent` and the protocol constants are available only from the ES module.

:::caution
Don't run `Tring.init` on a page that also runs the `agent.js` widget. The widget doesn't share its instance, so you would open a second connection for the same agent. Choose either the widget or the API.
:::

All tokens come from **your** backend. See the [overview](/tring/sdk/overview/#trust-model).

## Events

Every class has the same small event emitter:

```ts
on(type, listener): () => void   // returns a function that removes the listener
off(type, listener): void
```

```js
const off = tring.on("call:ended", ({ call }) => console.log("ended", call.to));
off(); // stop listening
```

If a listener throws, the error is logged as `[tring] listener error:` and the other listeners still run.

## Tring

The agent client. It connects to the gateway, registers the agent as online, and gives you a [`Call`](#call) whenever a call rings this agent.

### `Tring.init(opts)`

```ts
static init(opts: InitOpts): Promise<Tring>
```

Creates the client and starts connecting. The promise resolves once the token has been fetched and the WebSocket has been created. It does **not** wait for the agent to be online, so listen for `registered` to know when they are. It **throws** if `gatewayUrl` is missing, or if you pass neither `getToken` nor `token`.

At start-up, `init` also asks for the microphone, so later calls (including auto-answered ones) don't need a new permission prompt. If the user denies it, the SDK asks again when they accept a call.

| Option | Type | Default | Meaning |
|---|---|---|---|
| `gatewayUrl` | `string` | (required) | Gateway base URL, for example `https://gw.tring.example.com`. The SDK changes `http(s)` to `ws(s)` and connects to `/rtc`. |
| `getToken` | `() => Promise<string>` | | Returns a fresh agent JWT. It's called on every connect and reconnect, and again for each accepted call. **Preferred**, and it wins over `token`. |
| `token` | `string` | | A static agent JWT from `GET /v1/agents/{id}/token`. It expires after 5 minutes, after which reconnects and new calls fail. |
| `autoAnswer` | `boolean` | `true` | Answer ringing calls automatically. Set it to `false` to show your own Accept/Decline UI. |
| `iceServers` | `RTCIceServer[]` | `[]` | Extra STUN/TURN servers. The gateway also sends its own when the call connects. |

```js
const tring = await Tring.init({
  gatewayUrl: "https://gw.tring.example.com",
  autoAnswer: false,
  getToken: async () => {
    // Your endpoint: verifies the agent session, calls GET /v1/agents/{id}/token
    const r = await fetch("/tring/agent-token", { credentials: "same-origin" });
    if (!r.ok) throw new Error(`token endpoint ${r.status}`);
    return (await r.json()).token;
  },
});
```

### `tring.disconnect()`

```ts
disconnect(): Promise<void>
```

Takes the agent offline: closes the connection, stops reconnecting, and releases the microphone. Emits `unregistered`. A call already in progress uses its own connection and isn't hung up, so hang it up first.

```js
await tring.disconnect();
```

### Tring events

| Event | Payload | When |
|---|---|---|
| `registered` | `{}` | The agent is online. Fires again after every successful reconnect. |
| `reconnecting` | `{ attempt: number }` | The connection dropped unexpectedly. The SDK retries with backoff (1 s, doubling, up to 10 s) and fetches a fresh token each time. `attempt` is currently always `0`. |
| `unregistered` | `{ reason?: string }` | The connection was closed by `disconnect()`. `reason` is currently not set. |
| `call:ringing` | `{ call: Call }` | A call is ringing this agent. With `autoAnswer: true`, the SDK accepts it right away. |
| `call:answered` | `{ call: Call }` | Audio is flowing. `call.status` is now `"connected"`. |
| `call:ended` | `{ call: Call, cause?: string }` | The call ended: hung up, declined, taken by another agent, or ended by the server. `cause` is currently not set. |
| `call:remote-video` | `{ call: Call, stream: MediaStream \| null }` | The other party started sharing their screen (`stream`) or stopped (`null`). |
| `call:supervisor` | `{ call: Call, present: boolean, mode: string, name: string }` | A supervisor started (`present: true`) or stopped watching this call. `mode` is, for example, `"whisper"`. The customer is never told. |
| `error` | `{ error: Error }` | A signaling, microphone or token error. If accepting a call fails, you get `error`, then `call:ended` for that call. |

```js
tring.on("registered", () => setBadge("online"));
tring.on("reconnecting", () => setBadge("reconnecting"));
tring.on("call:ringing", ({ call }) => showIncoming(call.displayName ?? "Incoming call"));
tring.on("call:answered", ({ call }) => openRecordFor(call.to));
tring.on("call:ended", () => hidePanel());
tring.on("error", ({ error }) => console.warn(error.message));
```

## Call

The object you get in `call:ringing`, which is the same object in every later event for that call.

| Property | Type | Notes |
|---|---|---|
| `to` | `string` | The label your backend set for the call, such as `agent_label` or an IVR label. **Never a phone number.** It can be `""`. |
| `displayName` | `string \| undefined` | The same label, or `undefined` when it's empty. |
| `direction` | `"inbound" \| "outbound"` | Currently always `"inbound"`. |
| `status` | `"ringing" \| "connected" \| "ended"` | Updated in place as the call progresses. |
| `id` | `string \| undefined` | Declared in the type, but currently never set. |
| `muted` | `boolean` (read-only) | Whether the agent's microphone is muted. |
| `sharingScreen` | `boolean` (read-only) | Whether the agent is sharing their screen. |

### Methods

```ts
accept(): Promise<void>
```

Answers a ringing call: the agent joins the call over WebRTC. Call it from the Accept click so the microphone can be granted. It does nothing if the call is already accepted. It **rejects**, and emits `error` and then `call:ended`, if it can't get a token or the microphone.

```js
acceptBtn.onclick = () => call.accept().catch((e) => showError(e.message));
```

```ts
hangup(): Promise<void>
```

Ends the call. On a call that hasn't been accepted, it **declines** instead, and Tring rings the next agent straight away.

```js
endBtn.onclick = () => call.hangup();
```

```ts
sendDTMF(digit: string): Promise<void>
```

Sends a keypress (`0`–`9`, `*`, `#`) to the call's IVR. The digit goes over the signaling socket, not as an audio tone. It does nothing before the call is accepted.

```js
await call.sendDTMF("1");
```

```ts
setMuted(muted: boolean): void
```

Mutes or unmutes the agent's microphone. This only affects the agent's browser.

```js
call.setMuted(!call.muted);
```

```ts
hold(): void
resume(): void
```

`hold()` plays the tenant's hold music to the other party and mutes the agent. `resume()` reverses both. They're sent over the call socket, with no response, so update your UI right away.

```js
let held = false;
holdBtn.onclick = () => {
  held ? call.resume() : call.hold();
  held = !held;
};
```

```ts
transfer(target: { toSip?: string; toTeam?: string }): void
```

Blind-transfers the call to one agent (`toSip`, their `sip_username`) or to a team (`toTeam`, its name). When the target answers, this agent's call ends and you get `call:ended`. If the server rejects the transfer (target unavailable, or a transfer to yourself), the call carries on, and **this class doesn't currently report the failure**. Use `RtcCall`'s `transfer-failed` event if you need it.

To list targets, call `GET /v1/sdk/transfer-targets` with the agent JWT (see [Agent widget](/tring/sdk/agent-widget/#how-hold-and-transfer-work)).

```js
call.transfer({ toTeam: "billing" });
// or
call.transfer({ toSip: "agent-2002" });
```

```ts
shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void>
stopScreen(): void
```

Starts or stops sharing the agent's screen with the other party. `shareScreen` opens the browser's screen picker, so call it from a click. It rejects with `"call not active"` before the call is accepted, and rejects if the user cancels the picker. `constraints` is passed to `getDisplayMedia` and defaults to `{ video: true }`. Sharing also stops if the user ends it from the browser's own controls.

```js
shareBtn.onclick = async () => {
  if (call.sharingScreen) return call.stopScreen();
  try { await call.shareScreen({ video: true }); } catch { /* cancelled */ }
};
```

### Showing the caller's screen

Audio plays automatically. The SDK creates and removes its own hidden `<audio>` elements. **Video doesn't**: put a shared screen into your own `<video>` element.

```js
tring.on("call:remote-video", ({ stream }) => {
  videoEl.srcObject = stream; // null clears it
  videoEl.hidden = !stream;
});
```

## TringGuest

The visitor client. It places one call into an IVR flow. Nothing connects until you call `call()`.

### `new TringGuest(opts)`

```ts
new TringGuest(opts: GuestInitOpts)
```

| Option | Type | Meaning |
|---|---|---|
| `credentialsUrl` | `string` | Your endpoint that returns a [`GuestCredentialsResponse`](#guestcredentialsresponse). It's fetched with GET and `credentials: "include"` on **every** `call()`. |
| `fetchCredentials` | `() => Promise<GuestCredentialsResponse>` | Returns credentials yourself, for example with a POST that carries a CAPTCHA response. It wins over `credentialsUrl`. |
| `gatewayUrl` | `string` | Gateway base URL, if you don't fetch credentials. Fetched credentials win. |
| `flowId` | `string` | Flow UUID, if you don't fetch credentials. For a visitor token, the gateway uses the flow pinned in the token instead. |
| `token` | `string` | Call token, if you don't fetch credentials. It's single use and valid for 2 minutes, so prefer fetching. |
| `iceServers` | `RTCIceServer[]` | Extra STUN/TURN servers. Fetched `ice_servers` win. |

The gateway always requires a token. In practice, use `credentialsUrl` or `fetchCredentials` so each call gets a freshly minted one.

### GuestCredentialsResponse

What your endpoint returns, unchanged from `POST /v1/guest-credentials`:

```ts
interface GuestCredentialsResponse {
  gateway_url: string;
  flow_id: string;
  token?: string;
  ice_servers?: RTCIceServer[];
}
```

If your endpoint responds with a non-2xx status, `call()` fails with the `error` field of its JSON body, or with `credentials request failed (<status>)`.

### Methods and getters

```ts
call(): Promise<void>
```

Asks for the microphone, then fetches credentials, then connects and starts the call. Call it **directly from a click**: the microphone is requested before any network request because iOS Safari requires that. Calling it during a call does nothing. It emits `connecting`, `ringing` and `connected`, and **throws** (after emitting `error`) if the page isn't on HTTPS, the microphone is denied, fetching credentials fails, or there's no gateway URL.

```js
const guest = new TringGuest({ credentialsUrl: "/tring/visitor-credentials" });
callBtn.onclick = () => guest.call().catch((e) => showError(e.message));
```

```ts
hangup(): Promise<void>
```

Ends the call, or cancels it before it connects. It's safe to call at any time.

```ts
sendDTMF(digit: string): Promise<void>
```

Sends a keypress (`0`–`9`, `*`, `#`) to the IVR.

```js
keypad.onclick = (e) => guest.sendDTMF(e.target.dataset.digit);
```

```ts
mute(muted: boolean): Promise<void>
toggleMute(): boolean          // returns the new muted state
```

```js
muteBtn.textContent = guest.toggleMute() ? "Unmute" : "Mute";
```

```ts
shareScreen(constraints?: DisplayMediaStreamOptions): Promise<void>
stopScreen(): void
toggleScreen(): Promise<boolean>   // returns true if sharing is now on
```

Shares the visitor's screen with the agent. Call it from a click. `shareScreen` throws `"not in a call"` outside a call.

| Getter | Type | Meaning |
|---|---|---|
| `inCall` | `boolean` | A call is in progress, from the moment it starts connecting until it ends |
| `isMuted` | `boolean` | Whether the microphone is muted |
| `isSharingScreen` | `boolean` | Whether the visitor is sharing their screen |

### TringGuest events

| Event | Payload | When |
|---|---|---|
| `connecting` | `{}` | `call()` started: asking for the microphone and fetching credentials |
| `ringing` | `{}` | The connection to the gateway is being opened, and the call is waiting to be answered |
| `connected` | `{}` | Audio is flowing: the visitor is in the IVR |
| `ended` | `{ reason?: string }` | The call ended. `reason` is set when the server ended the call (`"ended"`, `"peer left"`, `"disconnected"`, …). **This can fire more than once for one call**, so make your handler idempotent. |
| `remote-video` | `{ stream: MediaStream \| null }` | The agent started or stopped sharing their screen |
| `error` | `{ error: Error }` | Something failed. See [Errors](#errors). |

A complete custom visitor button, using the `voice.js` global:

```html
<button id="call">Call us</button>
<button id="end" hidden>End</button>
<span id="status"></span>

<script src="https://tring.example.com/sdk/voice.js"></script>
<script>
  const guest = new TringGuest({ credentialsUrl: "/tring/visitor-credentials" });
  const $ = (id) => document.getElementById(id);

  guest.on("connecting", () => ($("status").textContent = "Connecting…"));
  guest.on("ringing", () => ($("status").textContent = "Calling…"));
  guest.on("connected", () => {
    $("status").textContent = "Connected";
    $("end").hidden = false;
  });
  guest.on("ended", () => {
    $("status").textContent = "Call ended";
    $("end").hidden = true;
  });
  guest.on("error", ({ error }) => ($("status").textContent = error.message));

  $("call").onclick = () => guest.call().catch(() => {});
  $("end").onclick = () => guest.hangup();
</script>
```

Loading `voice.js` without `data-*` attributes logs one console error ("provide data-gateway-url … or data-credentials-url") and renders no widget. That's expected here. With a bundler, import `TringGuest` instead.

## Low-level clients

`RtcCall` and `RtcAgent` speak the gateway's `/rtc` protocol directly. `Tring` and `TringGuest` are thin layers over them. You only need them for setups the classes above don't cover. They're available from the ES module only.

### RtcCall

One call: a WebSocket to `/rtc` plus a WebRTC connection.

```ts
new RtcCall(opts: RtcCallOpts)
```

| Option | Type | Default | Meaning |
|---|---|---|---|
| `gatewayUrl` | `string` | (required) | Gateway base URL |
| `room` | `string` | (required) | Room id to join. The gateway keeps rooms separate per tenant. |
| `token` | `string` | (required) | A visitor call token or an agent JWT |
| `getToken` | `() => Promise<string>` | | Fresh token for each connect (agents). It wins over `token`. |
| `name` | `string` | | Display name shown to the other participants |
| `flowId` | `string` | | Flow to run in the room. For visitor tokens, the flow in the token is used instead. |
| `iceServers` | `RTCIceServer[]` | `[]` | Added before the servers the gateway sends |
| `mic` | `MediaStream` | | A microphone stream you already have. If you pass it, `join()` doesn't call `getUserMedia`. |
| `keepMic` | `boolean` | `false` | Don't stop `mic` when the call ends, so you can reuse it |
| `reconnectAttempts` | `number` | `3` | How many times to rejoin after an unexpected drop. `0` turns rejoining off. |

| Method | Description |
|---|---|
| `join(): Promise<void>` | Gets the microphone (unless you passed `mic`) and connects. Call it inside a click. It rejects with a friendly microphone message. |
| `hangup(): void` | Ends the call and emits `ended`. It doesn't reconnect. |
| `sendDtmf(digit: string): void` | Sends a keypress to the room's IVR |
| `setMuted(muted: boolean): void` | Mutes or unmutes the outgoing microphone |
| `hold(): void` / `resume(): void` | Hold music on or off for the other party. These also mute or unmute you. |
| `transfer({ toSip?, toTeam? }): void` | Blind transfer |
| `shareScreen(constraints?): Promise<void>` | Starts sharing your screen. It throws `"not connected"` before the call has connected. |
| `stopScreen(): void` | Stops sharing your screen |
| `sharingScreen: boolean` | Read-only |

| Event | Payload |
|---|---|
| `connecting` | `{}`. Fires on each connect attempt. |
| `connected` | `{}`. The WebRTC connection is up. |
| `track` | `{ kind: "audio" \| "video", stream, track }`. Audio plays automatically; video is up to you. |
| `peer-joined` / `peer-left` | `{ participantId, name? }` / `{ participantId }`. When the last other participant leaves, the call ends with reason `"peer left"`. |
| `reconnecting` | `{}`. The SDK is rejoining after an unexpected drop. |
| `ended` | `{ reason?: string }` |
| `supervisor` | `{ present, mode, name }`. Agent calls only. |
| `transfer-failed` | `{ reason: string }`. The server rejected the transfer, and the call carries on. |
| `error` | `{ error: Error }` |

```js
import { RtcCall } from "@tring/voice";

const call = new RtcCall({ gatewayUrl, room: "support-1", token });
call.on("transfer-failed", ({ reason }) => toast(`Transfer failed: ${reason}`));
call.on("ended", ({ reason }) => console.log("ended", reason));
await call.join();
```

### RtcAgent

The connection that keeps an agent online. It hands you an `RtcCall` when you accept a call.

```ts
new RtcAgent(opts: RtcAgentOpts)
```

| Option | Type | Meaning |
|---|---|---|
| `gatewayUrl` | `string` | (required) Gateway base URL |
| `name` | `string` | (required) Display name used when joining calls |
| `token` / `getToken` | `string` / `() => Promise<string>` | Agent JWT, or a function that fetches a fresh one (preferred) |
| `iceServers` | `RTCIceServer[]` | Passed to each call |

| Method | Description |
|---|---|
| `goOnline(): Promise<void>` | Asks for the microphone (best effort) and connects. It reconnects with backoff until `goOffline()`. It throws `"agent token required (token or getToken)"` if it has no token. |
| `accept(): Promise<RtcCall \| null>` | Joins the ringing call and returns its `RtcCall`, or `null` if nothing is ringing |
| `decline(): void` | Rejects the ringing call, so Tring rings the next agent straight away |
| `hasIncoming(): boolean` | Whether a call is ringing |
| `goOffline(): void` | Disconnects, stops reconnecting, and releases the microphone |

| Event | Payload |
|---|---|
| `online` | `{}` |
| `reconnecting` | `{}` |
| `offline` | `{}`. Fires only after `goOffline()`. |
| `incoming` | `{ room: string, from?: string }`. `from` is the call's label. |
| `incoming-canceled` | `{ room: string }`. The ring was withdrawn: another agent answered, or the ring moved on. |
| `error` | `{ error: Error }` |

```js
import { RtcAgent } from "@tring/voice";

const agent = new RtcAgent({ gatewayUrl, name: "agent", getToken });
agent.on("incoming", ({ from }) => showRing(from));
acceptBtn.onclick = async () => {
  const call = await agent.accept();
  call?.on("ended", () => hidePanel());
};
await agent.goOnline();
```

## Errors

The SDK has no custom error classes. Every failure is a plain `Error`: it's passed to `error` listeners and, for the methods marked above, the promise rejects with it. Messages you may see:

| Message | Cause |
|---|---|
| `Tring.init: gatewayUrl is required` | `init` was called without `gatewayUrl` |
| ``Tring.init: provide `getToken` (preferred) or `token` `` | `init` was called with no token |
| `Microphone needs HTTPS — …` | The page isn't a secure context |
| `Microphone blocked — allow microphone access and try again.` | The user or browser denied the microphone (`RtcCall`) |
| `No microphone found on this device.` / `Your microphone is in use by another app.` | The device is missing or busy (`RtcCall`) |
| `credentials request failed (<status>)`, or your endpoint's `error` text | Your credentials endpoint failed (`TringGuest`) |
| `TringGuest: gatewayUrl (or credentialsUrl/fetchCredentials) is required` | `call()` had nowhere to connect |
| `signaling socket error` / `presence socket error` | The WebSocket to the gateway failed, for example a refused token (HTTP 401) or a network error |
| `token rejected` | A visitor token was already used. An expired or invalid token is refused at the WebSocket upgrade instead, and shows up as `signaling socket error`. |
| `call not active` / `not in a call` / `not connected` | Screen sharing was started outside a connected call |

`TringGuest` passes microphone errors through as the browser reported them (a `DOMException` such as `NotAllowedError`), so check `error.name` there.

## The /rtc protocol

Both clients use one WebSocket to `{gatewayUrl}/rtc?token=<jwt>` (`ws://` or `wss://`, from the gateway URL's scheme) for signaling. Audio and video go over WebRTC (DTLS-SRTP, Opus) directly to the gateway. FreeSWITCH stays private.

- **The first message sets the connection's role.** `register` makes it an agent's presence connection: the gateway replies `ready` and later sends `incoming` / `incoming-cancel`. `join` (with a `room`) makes it a call connection.
- **The gateway makes the WebRTC offer.** The client answers with `answer` and exchanges `ice` candidates as they're found, in both directions.
- **In-call messages** from the client: `dtmf`, `hold`, `resume`, `transfer`, `publish` / `unpublish` (screen sharing), and `bye`. From the server: `peer-joined`, `peer-left`, `peer-state`, `ended`, `supervisor`, `transfer-failed` and `error`.
- **Keepalive.** The gateway drops a socket that has been silent for about 90 seconds, so the SDK sends a `ping` (calls) or a repeated `register` (presence) every 25 seconds. The gateway answers each one with `pong`, so a reverse proxy in front of it sees traffic in both directions; nginx, for example, closes a WebSocket after 60 seconds without data from the upstream by default (`proxy_read_timeout`). Clients can ignore `pong`.
- **Rejoining.** `joined` returns a `session_token`. After a drop, the client sends it in its next `join` to get its place in the call back within the gateway's grace period.

All message types are exported as `ClientMsgType` and `ServerMsgType`, and the message shape as the `RtcMessage` type. The protocol's source of truth is `sdk/js/src/rtc-protocol.ts`, which mirrors `internal/media/signaling/protocol.go`. See the [architecture overview](/tring/architecture/overview/) for how the gateway fits in.
