export const tokenStorageKey=(system:string)=>`chill.token.v1:${encodeURIComponent(system)}`
export function readTabToken(storage:Pick<Storage,"getItem">,system:string){try{return storage.getItem(tokenStorageKey(system))??""}catch{return ""}}
export function storeTabToken(storage:Pick<Storage,"setItem"|"removeItem">,system:string,token:string){try{if(token)storage.setItem(tokenStorageKey(system),token);else storage.removeItem(tokenStorageKey(system));return true}catch{return false}}
