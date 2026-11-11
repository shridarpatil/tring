// Real-browser regression for the server-offerer contract. Start Chromium with
// --remote-debugging-port=9222 and run npm run test:browser-video. A container
// browser needs host networking to reach the temporary loopback page.
// Synthetic canvas cameras keep this independent of webcam permissions.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { build } from 'esbuild';

const debug = process.env.TRING_BROWSER_DEBUG_URL ?? 'http://127.0.0.1:9222';
const bundle = await build({ entryPoints: [new URL('../src/video.ts', import.meta.url).pathname], bundle: true, format: 'esm', write: false });
const moduleURL = `data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`;
const page = createServer((_req, res) => res.end('<!doctype html><title>Camera negotiation regression</title>'));
await new Promise(resolve => page.listen(0, '127.0.0.1', resolve));
let target, socket;
try {
  const response = await fetch(`${debug}/json/new?${encodeURIComponent(`http://127.0.0.1:${page.address().port}`)}`, { method: 'PUT' });
  target = await response.json();
  socket = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let id = 0;
  const pending = new Map();
  socket.onmessage = event => {
    const message = JSON.parse(event.data), job = pending.get(message.id);
    if (!job) return;
    pending.delete(message.id);
    message.error ? job.reject(message.error) : job.resolve(message.result);
  };
  const command = (method, params) => new Promise((resolve, reject) => {
    const seq = ++id; pending.set(seq, { resolve, reject });
    socket.send(JSON.stringify({ id: seq, method, params }));
  });
  const result = await command('Runtime.evaluate', {
    awaitPromise: true, returnByValue: true, timeout: 30000,
    expression: `(async () => {
      const { LocalVideo } = await import(${JSON.stringify(moduleURL)});
      const client = new RTCPeerConnection(), server = new RTCPeerConnection();
      const timers = [], streams = [];
      function camera() {
        const canvas = document.createElement('canvas'); canvas.width = 320; canvas.height = 240;
        const context = canvas.getContext('2d'); let frame = 0;
        const draw = () => { context.fillStyle = 'hsl(' + frame++ * 7 + ',80%,50%)'; context.fillRect(0,0,320,240); };
        draw(); timers.push(setInterval(draw, 40));
        const stream = canvas.captureStream(24); streams.push(stream); return stream;
      }
      navigator.mediaDevices.getUserMedia = async () => camera();
      let publishing = false;
      const video = new LocalVideo(() => client, value => publishing = value, () => {});
      const queuedClient = [], queuedServer = [];
      client.onicecandidate = e => { if (e.candidate) { if (server.remoteDescription) void server.addIceCandidate(e.candidate); else queuedServer.push(e.candidate); } };
      server.onicecandidate = e => { if (e.candidate) { if (client.remoteDescription) void client.addIceCandidate(e.candidate); else queuedClient.push(e.candidate); } };
      for (const pc of [client,server]) pc.ontrack = e => { const el=document.createElement('video');el.autoplay=true;el.muted=true;el.srcObject=new MediaStream([e.track]);document.body.append(el); };
      async function negotiate() {
        const offer = await server.createOffer(); await server.setLocalDescription(offer);
        await client.setRemoteDescription(offer); await video.answerOffer(offer.sdp);
        for (const c of queuedClient.splice(0)) await client.addIceCandidate(c);
        const answer = await client.createAnswer(); await client.setLocalDescription(answer); await server.setRemoteDescription(answer);
        for (const c of queuedServer.splice(0)) await server.addIceCandidate(c);
      }
      const frames = async pc => Array.from((await pc.getStats()).values()).filter(s=>s.type==='inbound-rtp'&&s.kind==='video').reduce((n,s)=>n+(s.framesDecoded||0),0);
      async function wait(pc, minimum) { const end=Date.now()+8000;while(Date.now()<end){if(await frames(pc)>minimum)return;await new Promise(r=>setTimeout(r,50));}throw Error('camera frames did not decode'); }
      try {
        await video.startCamera();
        if (!publishing || client.getTransceivers().length) throw Error('created an unnegotiated camera transceiver');
        const receive = server.addTransceiver('video',{direction:'recvonly'});
        await negotiate(); await wait(server,3);
        const first = await frames(server);
        server.addTransceiver(camera().getVideoTracks()[0],{direction:'sendonly'});
        await negotiate(); await wait(client,3);
        video.stop(); receive.stop(); await negotiate();
        await video.startCamera(); server.addTransceiver('video',{direction:'recvonly'});
        await negotiate(); await wait(server,first+3);
        return { clientFramesAtServer:await frames(server), serverFramesAtClient:await frames(client) };
      } finally {
        video.stop(); client.close(); server.close();timers.forEach(clearInterval);streams.forEach(s=>s.getTracks().forEach(t=>t.stop()));
      }
    })()`,
  });
  assert.equal(result.exceptionDetails, undefined, result.exceptionDetails?.exception?.description);
  assert.ok(result.result.value.clientFramesAtServer > 3);
  assert.ok(result.result.value.serverFramesAtClient > 3);
  console.log('Browser video negotiation and restart passed:', result.result.value);
} finally {
  socket?.close();
  if (target?.id) await fetch(`${debug}/json/close/${target.id}`);
  page.close();
}
