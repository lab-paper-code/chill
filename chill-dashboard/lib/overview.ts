import type { MeasurementCoverageResponse } from "./read-types.ts"

const deviceAliases:Record<string,string>={"edge-server-rtx-2080-ti-64g":"RTX 2080Ti","jetson-agx-orin-64g":"AGX Orin","jetson-nano-4g":"Nano","jetson-orin-nano-8g":"Orin Nano","jetson-xavier-nx-8g":"Xavier NX","lattepanda-3-delta-8g":"LattePanda","orangepi-5-plus-16g":"OPi5+","raspberry-pi-5-8g":"RPi5"}
const deviceOrder=Object.keys(deviceAliases)
export const overviewDeviceLabel=(name:string)=>deviceAliases[name]??name

export function overviewCoverageMatrix(groups:MeasurementCoverageResponse["groups"]) {
  const models=[...new Map(groups.map(group=>[group.model.uid,group.model])).values()]
  const devices=[...new Map(groups.map(group=>[group.deviceClass.uid,group.deviceClass])).values()].sort((a,b)=>{
    const left=deviceOrder.indexOf(a.name),right=deviceOrder.indexOf(b.name)
    return (left<0?deviceOrder.length:left)-(right<0?deviceOrder.length:right)||a.name.localeCompare(b.name)
  })
  return {devices,rows:models.map(model=>({model,cells:devices.map(device=>groups.find(group=>group.model.uid===model.uid&&group.deviceClass.uid===device.uid)??null)}))}
}
