import assert from 'node:assert/strict';
import {beforeEach,afterEach,test} from 'node:test';
import {build} from 'esbuild';
const bundle=await build({stdin:{contents:'export {TringGuest} from "./guest"; export {RtcCall} from "./rtc";',resolveDir:new URL('../src/',import.meta.url).pathname,loader:'ts'},bundle:true,format:'esm',write:false});
const {TringGuest,RtcCall}=await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const originals=new Map(['window','navigator'].map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]));
const originalJoin=RtcCall.prototype.join,originalHangup=RtcCall.prototype.hangup;
let rtc,guest,events;
beforeEach(()=>{
 globalThis.window={isSecureContext:true};
 Object.defineProperty(globalThis,'navigator',{configurable:true,value:{mediaDevices:{getUserMedia:async()=>({getTracks:()=>[{stop(){}}]})}}});
 RtcCall.prototype.join=async function(){rtc=this;};RtcCall.prototype.hangup=function(){};
 guest=new TringGuest({gatewayUrl:'https://test.invalid'});events=[];
 for(const name of ['connecting','ringing','connected','answered','ended'])guest.on(name,()=>events.push(name));
});
afterEach(async()=>{await guest.hangup();RtcCall.prototype.join=originalJoin;RtcCall.prototype.hangup=originalHangup;for(const[k,v]of originals){if(v)Object.defineProperty(globalThis,k,v);else delete globalThis[k]}});

test('IVR media connection does not count as agent answer',async()=>{
 await guest.call();rtc.emit({type:'connected'});
 assert.deepEqual(events,['connecting','ringing','connected']);
 rtc.emit({type:'answered'});assert.equal(events.at(-1),'answered');
});
test('reconnect and duplicate answers do not restart a call timer',async()=>{
 await guest.call();rtc.emit({type:'answered'});rtc.emit({type:'connected'});rtc.emit({type:'answered'});rtc.emit({type:'connected'});
 assert.equal(events.filter(e=>e==='answered').length,1);
});
test('a later call waits for its own answer',async()=>{
 await guest.call();rtc.emit({type:'answered'});await guest.hangup();events=[];
 await guest.call();rtc.emit({type:'connected'});assert.equal(events.includes('answered'),false);
 rtc.emit({type:'answered'});assert.equal(events.filter(e=>e==='answered').length,1);
});
