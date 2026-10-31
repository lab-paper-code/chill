import assert from "node:assert/strict"
import test from "node:test"
import { overviewCoverageMatrix, overviewDeviceLabel } from "./overview.ts"

test("coverage matrix retains unknown and zero denominators without merging model identities",()=>{
  const device={uid:"d",name:"jetson-nano-4g"},model={uid:"a",name:"same-name"}
  const matrix=overviewCoverageMatrix([
    {model,deviceClass:device,cellIds:["first"],complete:0,supported:0,unknownSupport:0},
    {model:{...model,uid:"b"},deviceClass:{uid:"e",name:"jetson-agx-orin-64g"},cellIds:["second"],complete:1,supported:null,unknownSupport:1},
  ])
  assert.equal(matrix.rows.length,2)
  assert.deepEqual(matrix.devices.map(row=>row.name),["jetson-agx-orin-64g","jetson-nano-4g"])
  assert.equal(matrix.rows[0].cells[0],null)
  assert.equal(matrix.rows[0].cells[1]?.supported,0)
  assert.equal(matrix.rows[1].cells[0]?.supported,null)
  assert.equal(overviewDeviceLabel(device.name),"Nano")
})
