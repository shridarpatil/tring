import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { build } from "esbuild";

// Bundle the production SDK so these tests exercise its actual signaling
// handlers without a browser, microphone, or live gateway.
const bundle = await build({entryPoints:[new URL('../src/rtc.ts',import.meta.url).pathname],bundle:true,format:'esm',write:false});
const { RtcAgent, RtcCall } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const originals = new Map(['window','WebSocket'].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]));
const originalJoin = RtcCall.prototype.join;
let agent, socket;
beforeEach(async()=>{
 globalThis.window={isSecureContext:false};
 globalThis.WebSocket=class {
  static OPEN=1;
  readyState=1;
  sent=[];
  constructor(){socket=this;}
  send(message){this.sent.push(JSON.parse(message));}
  close(){}
 };
 RtcCall.prototype.join=async function(){};
 agent=new RtcAgent({gatewayUrl:'https://test.invalid',token:'test'});
 await agent.goOnline();
});
afterEach(()=>{
 agent.goOffline();
 RtcCall.prototype.join=originalJoin;
 for(const [key,descriptor] of originals){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key];}
});
const receive=message=>socket.onmessage({data:JSON.stringify(message)});

test('a second ring is declined without overwriting the first or emitting another incoming event',()=>{
 const rings=[];agent.on('incoming',e=>rings.push(e.room));
 receive({type:'incoming',room:'first'});
 receive({type:'incoming',room:'second'});
 receive({type:'incoming',room:'first'}); // duplicate is harmless
 assert.deepEqual(rings,['first']);
 assert.deepEqual(socket.sent,[{type:'decline',room:'second'}]);
 agent.decline();
 assert.deepEqual(socket.sent.at(-1),{type:'decline',room:'first'});
});

test('a stale cancel cannot clear the current incoming call',()=>{
 const cancelled=[];agent.on('incoming-canceled',e=>cancelled.push(e.room));
 receive({type:'incoming',room:'first'});
 receive({type:'incoming-cancel',room:'old'});
 assert.equal(agent.hasIncoming(),true);
 assert.deepEqual(cancelled,[]);
 receive({type:'incoming-cancel',room:'first'});
 assert.equal(agent.hasIncoming(),false);
 assert.deepEqual(cancelled,['first']);
});

test('answering and connected calls reject a second ring until the active call ends',async()=>{
 let finishJoin;
 RtcCall.prototype.join=()=>new Promise(resolve=>{finishJoin=resolve});
 receive({type:'incoming',room:'first'});
 const accepting=agent.accept();
 receive({type:'incoming',room:'during-answer'});
 assert.deepEqual(socket.sent.at(-1),{type:'decline',room:'during-answer'});
 finishJoin();
 const active=await accepting;
 receive({type:'incoming-cancel',room:'first'});
 receive({type:'incoming',room:'while-connected'});
 assert.deepEqual(socket.sent.at(-1),{type:'decline',room:'while-connected'});
 active.emit({type:'ended'});
 receive({type:'incoming',room:'next'});
 assert.equal(agent.hasIncoming(),true);
});

test('failed answers release the local slot and decline the failed room',async()=>{
 RtcCall.prototype.join=async()=>{throw new Error('microphone unavailable')};
 receive({type:'incoming',room:'first'});
 await assert.rejects(agent.accept(),/microphone unavailable/);
 assert.deepEqual(socket.sent.at(-1),{type:'decline',room:'first'});
 receive({type:'incoming',room:'next'});
 assert.equal(agent.hasIncoming(),true);
});
