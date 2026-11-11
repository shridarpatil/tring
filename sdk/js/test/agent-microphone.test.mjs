import assert from 'node:assert/strict';
import {beforeEach, afterEach, test} from 'node:test';
import {build} from 'esbuild';

const bundle = await build({entryPoints:[new URL('../src/rtc.ts', import.meta.url).pathname],bundle:true,format:'esm',write:false});
const {RtcAgent} = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const originals = new Map(['window','navigator','WebSocket'].map(key => [key,Object.getOwnPropertyDescriptor(globalThis,key)]));
let agent, sockets, tracks, capture;
function stream() {
  const track = {readyState:'live', enabled:true, stop(){this.readyState='ended';}};
  tracks.push(track);
  return {getTracks:()=>[track], getAudioTracks:()=>[track]};
}
function receive(socket, message) { socket.onmessage({data:JSON.stringify(message)}); }
const live = () => tracks.filter(t => t.readyState === 'live');
const ring = room => receive(sockets[0], {type:'incoming',room});
beforeEach(() => {
  sockets=[]; tracks=[]; capture=async()=>stream();
  globalThis.window={isSecureContext:true};
  Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{getUserMedia:opts=>{assert.deepEqual(opts,{audio:true});return capture();}}}});
  globalThis.WebSocket=class {
    static OPEN=1;
    readyState=1;
    sent=[];
    constructor(){sockets.push(this);}
    send(data){this.sent.push(JSON.parse(data));}
    close(){this.readyState=3;}
  };
  agent=new RtcAgent({gatewayUrl:'https://test.invalid',token:'test',name:'agent'});
});
afterEach(()=>{
  agent.goOffline();
  for(const [key,value] of originals){if(value)Object.defineProperty(globalThis,key,value);else delete globalThis[key];}
});

test('initial permission check releases capture; ringing, declining and cancellation leave the mic off',async()=>{
  await agent.goOnline();
  assert.equal(tracks.length,1);
  assert.equal(live().length,0);
  ring('declined'); agent.decline();
  ring('cancelled'); receive(sockets[0],{type:'incoming-cancel',room:'cancelled'});
  assert.equal(tracks.length,1);
  assert.equal(live().length,0);
});

test('each accepted call gets a fresh mic and releases it on local or remote hangup',async()=>{
  await agent.goOnline();
  ring('first');const first=await agent.accept();
  const firstTrack=live()[0];assert.ok(firstTrack);
  first.hangup();assert.equal(live().length,0);
  ring('second');await agent.accept();
  assert.equal(live().length,1);assert.notEqual(live()[0],firstTrack);
  receive(sockets.at(-1),{type:'ended'});
  assert.equal(live().length,0);
});

test('a failed call token fetch releases the newly captured microphone',async()=>{
  let tokenRequests=0;
  agent=new RtcAgent({gatewayUrl:'https://test.invalid',name:'agent',getToken:async()=>{
    if(++tokenRequests>1)throw Error('token unavailable');return 'test';
  }});
  await agent.goOnline();ring('first');
  await assert.rejects(agent.accept(),/token unavailable/);
  assert.equal(live().length,0);
  assert.deepEqual(sockets[0].sent.at(-1),{type:'decline',room:'first'});
});

test('denied startup permission is retried on accept without holding an idle stream',async()=>{
  capture=async()=>{throw new DOMException('denied','NotAllowedError');};
  await agent.goOnline();assert.equal(live().length,0);
  capture=async()=>stream();ring('first');await agent.accept();
  assert.equal(live().length,1);
  agent.goOffline();assert.equal(live().length,0);
});

test('denied call permission fails once, releases the slot, and allows the next call',async()=>{
  await agent.goOnline();let attempts=0;
  capture=async()=>{attempts++;throw new DOMException('denied','NotAllowedError');};
  ring('first');await assert.rejects(agent.accept(),/Microphone blocked/);
  assert.equal(attempts,1);assert.equal(live().length,0);
  capture=async()=>stream();ring('second');await agent.accept();
  assert.equal(live().length,1);
});

test('going offline while call capture is pending stops the late stream without opening a call socket',async()=>{
  await agent.goOnline();let resolve;
  capture=()=>new Promise(r=>resolve=r);
  ring('first');const accepting=agent.accept();agent.goOffline();resolve(stream());
  assert.equal(await accepting,null);
  assert.equal(live().length,0);assert.equal(sockets.length,1);
});

test('going offline during the initial permission prompt releases its late stream and stays offline',async()=>{
  let resolve;capture=()=>new Promise(r=>resolve=r);
  const connecting=agent.goOnline();agent.goOffline();resolve(stream());await connecting;
  assert.equal(live().length,0);assert.equal(sockets.length,0);
});

test('taking over presence after the permission check does not capture another microphone',async()=>{
  await agent.goOnline();await agent.goOnline();
  assert.equal(tracks.length,1);assert.equal(live().length,0);
});
