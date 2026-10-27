import { currentRoutingScope, routingPrincipalAllowed, type RoutingScope } from '../api';
import { useStore } from '../store';
// Captured action facade. Each subsequent request in a multi-await gesture
// checks again, and no response from an obsolete view is applied.
export function viewIsCurrent(scope?: RoutingScope, generation?: number): boolean {
 return routingPrincipalAllowed() && (!scope || scope.key === currentRoutingScope().key) && (generation === undefined || generation === useStore.getState().navigationGeneration);
}
export function guardViewApi<T extends object>(source:T, valid:()=>boolean):T {
 return new Proxy(source,{get(target,property,receiver) {
  const value = Reflect.get(target,property,receiver);
  if(typeof value !== 'function') return value;
  return (...args:unknown[])=> {
   if(!valid()) return Promise.reject(new Error('This view is no longer active. Reopen it to act.'));
   const result = Reflect.apply(value as (...args:unknown[])=>unknown,target,args) as {then?:(fn:(response:unknown)=>unknown)=>unknown} | undefined;
   return result && typeof result.then === 'function' ? result.then((response:unknown)=> {
    if(!valid()) throw new Error('Response belongs to an obsolete view');
    return response;
   }) : result;
  };
 }});
}
