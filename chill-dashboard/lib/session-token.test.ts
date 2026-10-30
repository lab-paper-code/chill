import assert from "node:assert/strict"
import test from "node:test"
import { readTabToken, storeTabToken, tokenStorageKey } from "./session-token.ts"

test("tab token uses versioned system scope and explicit removal",()=>{
  const values=new Map<string,string>()
  const storage={getItem:(key:string)=>values.get(key)??null,setItem:(key:string,value:string)=>{values.set(key,value)},removeItem:(key:string)=>{values.delete(key)}}
  assert.equal(tokenStorageKey("a/b"),"chill.token.v1:a%2Fb")
  assert.equal(storeTabToken(storage,"system","secret"),true)
  assert.equal(readTabToken(storage,"system"),"secret")
  assert.equal(readTabToken(storage,"other"),"")
  storeTabToken(storage,"other","other-secret")
  storeTabToken(storage,"system","")
  assert.equal(readTabToken(storage,"system"),"")
  assert.equal(readTabToken(storage,"other"),"other-secret")
})
test("denied tab storage keeps memory-only operation available",()=>{
  const denied={getItem:()=>{throw new Error("denied")},setItem:()=>{throw new Error("denied")},removeItem:()=>{throw new Error("denied")}}
  assert.equal(readTabToken(denied,"system"),"")
  assert.equal(storeTabToken(denied,"system","secret"),false)
  assert.equal(storeTabToken(denied,"system",""),false)
})
