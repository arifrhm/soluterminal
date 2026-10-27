import { useCallback, useEffect, useRef, useState } from 'react';
import { api, currentRoutingScope, onRoutingScopeChange, type WorkflowSettings } from '../../api';
import { ModelGatewaySection } from './ModelGatewaySection';
import { ReleaseTrainSection } from './ReleaseTrainSection';
import { ResourceLimitsSection } from './ResourceLimitsSection';
export type WorkflowSettingsUpdate = (mutate: (current: WorkflowSettings) => WorkflowSettings) => Promise<WorkflowSettings>;
export function ServicesTab() {
 const [scope,setScope]=useState(currentRoutingScope);
 const [settings,setSettings]=useState<WorkflowSettings|null>(null);
 const [error,setError]=useState('');
 const saveTail=useRef<Promise<void>>(Promise.resolve());
 useEffect(()=>onRoutingScopeChange(()=>{setSettings(null);setError('');setScope(currentRoutingScope())}),[]);
 useEffect(()=>{let alive=true;api.getWorkflowSettings(undefined,scope).then(s=>{if(alive)setSettings(s)}).catch(e=>{if(alive)setError(String(e))});return()=>{alive=false}},[scope]);
 const update=useCallback<WorkflowSettingsUpdate>(mutate=>{
  const captured=scope;
  const active=()=>currentRoutingScope().key===captured.key;
  const operation=saveTail.current.then(async()=>{
   if(!active())throw new Error('Machine changed; reopen service settings');
   const current=await api.getWorkflowSettings(undefined,captured);
   if(!active())throw new Error('Machine changed; reopen service settings');
   const next=await api.setWorkflowSettings(mutate(current),current,captured);
   if(active())setSettings(next);
   return next;
  });
  saveTail.current=operation.then(()=>undefined,()=>undefined);return operation;
 },[scope]);
 if(error)return <p role="alert">{error}</p>;
 if(!settings)return <p>Loading service settings…</p>;
 return <div key={scope.key}><ModelGatewaySection settings={settings} update={update}/><ReleaseTrainSection sharedSettings={settings} updateSharedSettings={update}/><ResourceLimitsSection sharedSettings={settings} updateSharedSettings={update}/></div>;
}
