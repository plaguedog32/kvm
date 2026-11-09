import { test } from "node:test";
import assert from "node:assert/strict";
import { NativeClipboardKeys } from "../src/utils/nativeClipboardKeys.ts";

test("missing Cmd+C keyup cannot swallow a later ordinary c release", () => {
  const keys = new NativeClipboardKeys();
  keys.swallowed.add("KeyC");
  keys.down({ code: "KeyC", repeat: false });
  assert.equal(keys.swallowed.delete("KeyC"), false);
  keys.swallowed.add("KeyV");
  keys.up({ code: "MetaLeft", key: "Meta" });
  assert.equal(keys.swallowed.size, 0);
});

test("physical Ctrl remains available to restore a subsequent Ctrl+S chord", () => {
  const keys = new NativeClipboardKeys();
  keys.down({ code: "ControlRight", repeat: false });
  keys.down({ code: "KeyV", repeat: false });
  keys.swallowed.add("KeyV");
  keys.prepareRelease();
  keys.up({ code: "KeyV", key: "v" });
  keys.down({ code: "KeyS", repeat: false });
  assert.deepEqual(keys.takeRestore({code:"KeyS",altKey:false}), ["ControlRight"]);
  assert.deepEqual(keys.takeRestore({code:"KeyZ",altKey:false}), []);
  keys.up({ code: "ControlRight", key: "Control" });
  assert.equal(keys.modifiers.size, 0);
});

test("after copy, synthetic AltGr Ctrl is never restored ahead of buffering",()=>{
  const keys=new NativeClipboardKeys();
  keys.down({code:"ControlLeft",repeat:false});keys.prepareRelease();
  keys.up({code:"ControlLeft",key:"Control"});
  keys.down({code:"ControlLeft",repeat:false});
  keys.finishRelease();
  assert.deepEqual(keys.takeRestore({code:"ControlLeft",altKey:false}),[]);
  keys.down({code:"AltRight",repeat:false});
  assert.deepEqual(keys.takeRestore({code:"AltRight",altKey:true}),[]);
  keys.down({code:"KeyQ",repeat:false});
  assert.deepEqual(keys.takeRestore({code:"KeyQ",altKey:true}),[]);
  keys.up({code:"ControlLeft",key:"Control"});keys.up({code:"AltRight",key:"AltGraph"});
  assert.deepEqual(keys.takeRestore({code:"KeyS",altKey:false}),[]);
});

test("a modifier still held across asynchronous HID completion can be restored once again",()=>{
 const keys=new NativeClipboardKeys();
 keys.down({code:"ControlRight",repeat:false});keys.prepareRelease();
 assert.deepEqual(keys.takeRestore({code:"KeyS",altKey:false}),["ControlRight"]);
 keys.finishRelease();
 assert.deepEqual(keys.takeRestore({code:"KeyZ",altKey:false}),["ControlRight"]);
 assert.deepEqual(keys.takeRestore({code:"KeyS",altKey:false}),[]);
});

test("Ctrl release before held C/V cannot leak repeating presses or swallow a guest release",()=>{
 for(const code of ["KeyC","KeyV"]){
  const keys=new NativeClipboardKeys();
  keys.down({code:"ControlLeft",repeat:false});keys.down({code,repeat:false});keys.swallowed.add(code);
  keys.up({code:"ControlLeft",key:"Control"});
  for(let i=0;i<10;i++) assert.equal(keys.down({code,repeat:true}),true);
  assert.equal(keys.up({code,key:code.slice(-1).toLowerCase()}),true);
  assert.equal(keys.down({code,repeat:false}),false);
  assert.equal(keys.up({code,key:"c"}),false);
 }
});

test("blur reset clears modifier and shortcut bookkeeping", () => {
  const keys = new NativeClipboardKeys();
  keys.down({ code: "MetaLeft", repeat: false });
  keys.swallowed.add("KeyC");
  keys.clear();
  assert.equal(keys.modifiers.size + keys.swallowed.size, 0);
});
