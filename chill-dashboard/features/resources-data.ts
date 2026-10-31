import type { LiveState } from "../lib/live.ts"
import { belongsToPool } from "../lib/live-display.ts"
import type { InventoryClass, InventoryView, NodeRef, PoolsView, SchedulerSnapshot, WorkloadRef, InstanceState } from "../lib/read-types.ts"

export type DeviceRow = NodeRef & {configured:boolean;classes:InventoryClass[];ready:boolean|null;observedAt:string|null;watts:number|null;powerAt:string|null;powerMode:string;roles:string[];reasons:string[];services:WorkloadRef[];instances:string[];poolInstances:InstanceState[];schedulingDisabled:boolean|null}

// Identity joins are UID-only: replaced nodes with the same name remain separate rows.
export function deviceRows(state:LiveState,inventory?:InventoryView,pools?:PoolsView,resources?:SchedulerSnapshot):DeviceRow[] {
  const rows=new Map<string,DeviceRow>(),metric=state.metrics.at(-1)
  function add(node:NodeRef) {
    let row=rows.get(node.uid)
    if(!row){row={...node,configured:false,classes:[],ready:null,observedAt:null,watts:null,powerAt:null,powerMode:"",roles:[],reasons:[],services:[],instances:[],poolInstances:[],schedulingDisabled:null};rows.set(node.uid,row)}
    if(node.name)row.name=node.name
    return row
  }
  function role(row:DeviceRow,label:string){if(!row.roles.includes(label))row.roles.push(label)}
  function service(row:DeviceRow,workload:WorkloadRef){if(!row.services.some(item=>item.uid===workload.uid))row.services.push(workload)}
  function instance(row:DeviceRow,id:string){if(!row.instances.includes(id))row.instances.push(id)}
  for(const cls of inventory?.classes??[])for(const node of cls.nodes){const row=add(node);row.classes.push(cls);row.ready=node.ready==="True"?true:node.ready==="False"?false:null;row.schedulingDisabled=node.schedulingDisabled;row.observedAt=inventory!.readAt}
  for(const pool of pools?.workloads??[]) {
    for(const node of pool.nodes??[]){const row=add(node);role(row,"서비스 배치");service(row,pool.workload)}
    for(const item of pool.instances??[]){const row=add(item.node);instance(row,item.id);service(row,pool.workload);for(const workload of item.workloads??[])service(row,workload);if(!row.poolInstances.some(value=>value.id===item.id))row.poolInstances.push(item);role(row,"서비스 배치");row.observedAt=pools!.observedAt}
  }
  for(const item of pools?.measurement??[]){const row=add(item.node);role(row,"측정 중");row.observedAt=pools!.observedAt;for(const value of item.instances??[]){instance(row,value.id);if(!row.poolInstances.some(item=>item.id===value.id))row.poolInstances.push(value);for(const workload of value.workloads??[])service(row,workload)}}
  for(const [items,label] of [[pools?.available,"가용"],[pools?.other,"제외·기타"]] as const)for(const item of items??[]){const row=add(item.node);role(row,label);row.ready=item.ready;row.powerMode=item.powerMode;row.observedAt=pools!.observedAt;row.reasons.push(...(item.reasons??[]))}
  for(const node of resources?.nodes??[]){const row=add(node.node);row.ready??=node.ready;row.observedAt??=node.observedAt;row.powerMode||=node.powerMode;row.reasons.push(...(node.reasons??[]));if(node.measurement)role(row,"측정 중")}
  for(const item of resources?.instances??[]){const row=add(item.node);instance(row,item.id);role(row,"서비스 배치");const definition=resources?.workloads?.find(value=>value?.uid===item.workloadUID);service(row,{uid:item.workloadUID,name:definition?.name??""})}
  for(const node of state.snapshot?.nodes??[]){const row=add(node);row.configured=true;row.ready=node.ready;row.powerMode=node.powerMode;row.observedAt=node.observedAt;row.reasons.push(...(node.reasons??[]));if(node.measurement)role(row,"측정 중")}
  for(const item of state.snapshot?.instances??[]){const row=add(item.node);instance(row,item.id);for(const workload of state.snapshot?.workloads??[])if(belongsToPool(item,workload.uid))service(row,workload)}
  for(const [uid,power] of Object.entries(metric?.nodes??{})){const row=add({uid,name:""});row.watts=power.watts;row.powerAt=power.observedAt}
  for(const row of rows.values())row.reasons=[...new Set(row.reasons)]
  return [...rows.values()].sort((a,b)=>(a.name||a.uid).localeCompare(b.name||b.uid,"ko")||a.uid.localeCompare(b.uid))
}
export function devicePowerScale(rows:DeviceRow[],retainedScale:number):number {return Math.max(1,retainedScale,...rows.map(row=>row.watts??0))}
export function devicePowerPercent(row:DeviceRow,scale:number):number|null {return row.watts===null?null:Math.max(0,Math.min(100,row.watts/scale*100))}
