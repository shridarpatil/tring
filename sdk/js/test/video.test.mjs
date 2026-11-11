import assert from 'node:assert/strict';
import { beforeEach, afterEach, test } from 'node:test';
import { build } from 'esbuild';

const bundle = await build({ entryPoints: [new URL('../src/video.ts', import.meta.url).pathname], bundle: true, format: 'esm', write: false });
const { LocalVideo } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const originals = new Map(['window', 'navigator'].map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
let pc, video, signals, changes, media;
function capture() {
  const track = { stopped: false, stop() { this.stopped = true; } };
  return { track, getTracks: () => [track], getVideoTracks: () => [track] };
}
beforeEach(() => {
  signals = []; changes = [];
  pc = {
    transceivers: [], connectionState: 'connected',
    addTransceiver(track, options) {
      assert.equal(options.direction, 'sendonly');
      const t = { sender: { track, async replaceTrack(next) { this.track = next; } }, stop() { this.stopped = true; } };
      this.transceivers.push(t); return t;
    },
    getTransceivers() { return this.transceivers; },
  };
  media = { getUserMedia: async () => capture(), getDisplayMedia: async () => capture() };
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { mediaDevices: media } });
  globalThis.window = { isSecureContext: true };
  video = new LocalVideo(() => pc, p => signals.push(p), state => changes.push(state));
});
afterEach(() => {
  video.stop();
  for (const [key, descriptor] of originals) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key];
  }
});

test('camera is opt-in, requests no second microphone, and can restart', async () => {
  assert.equal(video.stream, null);
  media.getUserMedia = async options => { assert.equal(options.audio, false); return capture(); };
  await video.startCamera();
  const first = video.stream;
  video.stop();
  assert.equal(first.track.stopped, true);
  assert.equal(pc.transceivers[0].stopped, true);
  await video.startCamera();
  assert.equal(pc.transceivers.length, 2);
  assert.deepEqual(signals, [true, false, true]);
  assert.equal(changes.at(-1).source, 'camera');
});

test('camera and screen replace the same sender and release the previous device', async () => {
  await video.startCamera();
  const camera = video.stream;
  await video.shareScreen();
  assert.equal(camera.track.stopped, true);
  assert.equal(pc.transceivers.length, 1);
  assert.equal(pc.transceivers[0].sender.track, video.stream.track);
  assert.equal(video.source, 'screen');
  video.stop('camera');
  assert.equal(video.source, 'screen');
  video.stream.track.onended();
  assert.equal(video.stream, null);
  assert.deepEqual(signals, [true, false]);
});

test('denied camera permission preserves a running screen share', async () => {
  await video.shareScreen();
  const screen = video.stream;
  media.getUserMedia = async () => { throw new Error('permission denied'); };
  await assert.rejects(video.startCamera(), /permission denied/);
  assert.equal(video.stream, screen);
  assert.equal(screen.track.stopped, false);
  assert.deepEqual(signals, [true]);
});

test('capture completing after hangup is stopped without publishing', async () => {
  let resolve;
  media.getUserMedia = () => new Promise(r => { resolve = r; });
  const pending = video.startCamera();
  await assert.rejects(video.shareScreen(), /already in progress/);
  video.stop();
  const late = capture(); resolve(late);
  await assert.rejects(pending, /cancelled/);
  assert.equal(late.track.stopped, true);
  assert.equal(video.stream, null);
  assert.deepEqual(signals, []);
});

test('failed source replacement keeps the old stream alive', async () => {
  await video.startCamera();
  const camera = video.stream, screen = capture();
  media.getDisplayMedia = async () => screen;
  pc.transceivers[0].sender.replaceTrack = async () => { throw new Error('replacement failed'); };
  await assert.rejects(video.shareScreen(), /replacement failed/);
  assert.equal(video.stream, camera);
  assert.equal(camera.track.stopped, false);
  assert.equal(screen.track.stopped, true);
});

test('failed publish signaling retires its transceiver and releases the camera', async () => {
  const camera = capture();
  media.getUserMedia = async () => camera;
  video = new LocalVideo(() => pc, () => { throw new Error('socket closed'); }, state => changes.push(state));
  await assert.rejects(video.startCamera(), /socket closed/);
  assert.equal(camera.track.stopped, true);
  assert.equal(pc.transceivers[0].stopped, true);
  assert.equal(video.stream, null);
});
