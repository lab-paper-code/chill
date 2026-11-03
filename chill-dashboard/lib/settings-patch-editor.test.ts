import assert from "node:assert/strict"
import test from "node:test"
import { elements, hookRunner, load } from "./evaluation-author-test-helpers.ts"
import * as author from "./evaluation-author.ts"
import * as scheduler from "./scheduler.ts"
import * as settings from "./scheduler-settings.ts"

const primitives={"@/components/ui/button":{Button:"button"},"@/components/ui/input":{Input:"input"},"@/lib/evaluation-author":author,"@/lib/scheduler":scheduler,"@/lib/scheduler-settings":settings}

test("settings patch choices exactly reuse operating settings and retain opaque precise JSON",()=>{
  const runner=hookRunner(),validity:boolean[]=[]
  const {SettingsPatchEditor}=load("../features/settings-patch-editor.tsx",{...primitives,react:runner.react})
  const props={spec:{settingsPatch:{future:{exact:9007199254740993n}}} as author.Spec,path:["settingsPatch"],onChange:(value:author.Spec)=>{props.spec=value},onValidityChange:(valid:boolean)=>validity.push(valid)}
  let tree=elements(runner.render(SettingsPatchEditor,props))
  const picker=tree.find(item=>item.props.label==="설정 항목")!
  assert.deepEqual(elements(picker).filter(item=>item.type==="option"&&item.props.value).map(item=>item.props.value),settings.editableSettingKeys)
  ;(picker.props.onChange as (key:string)=>void)("observationWindowNanos")
  tree=elements(runner.render(SettingsPatchEditor,props))
  ;(tree.find(item=>item.type==="textarea")!.props.onChange as (event:unknown)=>void)({target:{value:"9007199254.740993"}})
  tree=elements(runner.render(SettingsPatchEditor,props))
  assert.equal(tree.find(item=>item.props.label==="설정 패치 원문 (ns)")?.props.disabled,true)
  assert.equal(validity.at(-1),false)
  ;(tree.find(item=>item.props.children==="항목 반영")!.props.onClick as ()=>void)()
  assert.deepEqual(props.spec.settingsPatch,{future:{exact:9007199254740993n},observationWindowNanos:9007199254740993n})
  assert.equal(validity.at(-1),true)
  tree=elements(runner.render(SettingsPatchEditor,props))
  ;(tree.find(item=>item.props.children==="패치 미설정")!.props.onClick as ()=>void)()
  assert.equal(Object.hasOwn(props.spec,"settingsPatch"),false)
})

test("P2 author: inherited null or omitted stage patches differ from explicit empty replacement after last-key removal",()=>{
  for(const value of [undefined,null,{}, {maxReplicas:2}]) {
    const runner=hookRunner(),props={spec:{settingsPatch:{maxReplicas:8},stages:[{id:"a",...(value===undefined?{}:{settingsPatch:value})}]} as author.Spec,path:["stages",0,"settingsPatch"],onChange:(value:author.Spec)=>{props.spec=value},onValidityChange:()=>{}}
    const {SettingsPatchEditor}=load("../features/settings-patch-editor.tsx",{...primitives,react:runner.react})
    let tree=elements(runner.render(SettingsPatchEditor,props))
    assert.ok(tree.some(item=>item.props.children===(value==null?"전체 실험 설정 상속":"단계 패치로 전체 대체")))
    if(value&&Object.hasOwn(value,"maxReplicas")) {
      ;(tree.find(item=>item.props.label==="설정 항목")!.props.onChange as (key:string)=>void)("maxReplicas")
      tree=elements(runner.render(SettingsPatchEditor,props))
      ;(tree.find(item=>item.props.children==="항목 제거")!.props.onClick as ()=>void)()
      tree=elements(runner.render(SettingsPatchEditor,props))
      assert.deepEqual(author.specValue(props.spec,props.path),{})
      assert.ok(tree.some(item=>item.props.children==="단계 패치로 전체 대체"))
    }
    ;(tree.find(item=>item.props.children==="전체 실험 설정 상속")?.props.onClick as (()=>void)|undefined)?.()
    tree=elements(runner.render(SettingsPatchEditor,props))
    const unset=tree.find(item=>item.type==="button"&&item.props.children==="패치 미설정")
    if(unset)(unset.props.onClick as ()=>void)()
    assert.equal(author.specValue(props.spec,props.path),undefined)
    tree=elements(runner.render(SettingsPatchEditor,props))
    ;(tree.find(item=>item.type==="button"&&item.props.children==="빈 단계 패치로 전체 대체")!.props.onClick as ()=>void)()
    assert.deepEqual(author.specValue(props.spec,props.path),{})
  }
})

test("P2 author: patch durations use exact operating milliseconds and do not add a second help tooltip",()=>{
  for(const field of settings.settingFields.filter(field=>field.key.endsWith("Nanos"))) {
    const runner=hookRunner(),props={spec:{settingsPatch:{[field.key]:9007199254740993n}} as author.Spec,path:["settingsPatch"],onChange:(value:author.Spec)=>{props.spec=value},onValidityChange:()=>{}}
    const {SettingsPatchEditor}=load("../features/settings-patch-editor.tsx",{...primitives,react:runner.react})
    let tree=elements(runner.render(SettingsPatchEditor,props))
    assert.ok(!tree.some(item=>item.props.children==="패치 도움말"))
    ;(tree.find(item=>item.props.label==="설정 항목")!.props.onChange as (key:string)=>void)(field.key)
    tree=elements(runner.render(SettingsPatchEditor,props))
    assert.ok(tree.some(item=>item.type==="label"&&elements(item).some(item=>item.type==="textarea")&&JSON.stringify(item.props.children).includes("ms")))
    assert.equal(tree.find(item=>item.type==="textarea")?.props.value,"9007199254.740993")
    ;(tree.find(item=>item.type==="textarea")!.props.onChange as (event:unknown)=>void)({target:{value:"1000.000001"}})
    tree=elements(runner.render(SettingsPatchEditor,props))
    ;(tree.find(item=>item.props.children==="항목 반영")!.props.onClick as ()=>void)()
    assert.equal(author.specValue(props.spec,["settingsPatch",field.key]),1000000001n)
  }
})

test("P2 author: invalid raw nanoseconds clear the previous setting value without throwing",()=>{
  for(const invalid of [1.5,Number.POSITIVE_INFINITY,Number.NaN]) {
    const runner=hookRunner(),validity:boolean[]=[],props={spec:{settingsPatch:{maxReplicas:4,controlIntervalNanos:invalid}} as author.Spec,path:["settingsPatch"],onChange:(value:author.Spec)=>{props.spec=value},onValidityChange:(value:boolean)=>validity.push(value)}
    const {SettingsPatchEditor}=load("../features/settings-patch-editor.tsx",{...primitives,react:runner.react})
    let tree=elements(runner.render(SettingsPatchEditor,props))
    ;(tree.find(item=>item.props.label==="설정 항목")!.props.onChange as (key:string)=>void)("maxReplicas")
    tree=elements(runner.render(SettingsPatchEditor,props))
    assert.equal(tree.find(item=>item.type==="textarea")?.props.value,"4")
    assert.doesNotThrow(()=>(tree.find(item=>item.props.label==="설정 항목")!.props.onChange as (key:string)=>void)("controlIntervalNanos"))
    tree=elements(runner.render(SettingsPatchEditor,props))
    assert.equal(tree.find(item=>item.type==="textarea")?.props.value,"")
    assert.equal(tree.find(item=>item.type==="textarea")?.props["aria-invalid"],true)
    assert.ok(tree.some(item=>item.props.role==="alert"&&typeof item.props.children==="string"))
    assert.equal(validity.at(-1),false)
    ;(tree.find(item=>item.props.children==="항목 반영")!.props.onClick as ()=>void)()
    assert.equal(author.specValue(props.spec,["settingsPatch","controlIntervalNanos"]),invalid)
    tree=elements(runner.render(SettingsPatchEditor,props))
    ;(tree.find(item=>item.type==="textarea")!.props.onChange as (event:unknown)=>void)({target:{value:"1000"}})
    tree=elements(runner.render(SettingsPatchEditor,props))
    ;(tree.find(item=>item.props.children==="항목 반영")!.props.onClick as ()=>void)()
    assert.equal(author.specValue(props.spec,["settingsPatch","controlIntervalNanos"]),1000000000n)
    assert.equal(author.specValue(props.spec,["settingsPatch","maxReplicas"]),4)
    assert.equal(validity.at(-1),true)
  }
})
