import { useState } from 'react';
import type { WorkflowSettings } from '../../api';
import type { WorkflowSettingsUpdate } from './ServicesTab';
import { SettingsGroup, SettingsRow } from './SettingsRow';
export function ModelGatewaySection({settings,update}:{settings:WorkflowSettings;update:WorkflowSettingsUpdate}){
 const [error,setError]=useState('');
 const saved=settings.localOpenAI;
 const save=async(key:'baseUrl'|'model',value:string)=>{try{await update(s=>({...s,localOpenAI:{...s.localOpenAI,[key]:value}}));setError('');return true}catch(e){setError(String(e));return false}};
 // Each box shows the saved value (keyed by it, so a change made elsewhere
 // shows) and saves only when it was actually edited, never a stale copy.
 const field=(key:'baseUrl'|'model',label:string)=>{const current=saved?.[key]??'';return <input key={`${key}:${current}`} aria-label={label} defaultValue={current} onBlur={async e=>{const input=e.currentTarget;const value=input.value;if(value!==current&&!await save(key,value)&&input.isConnected)input.value=current}}/>};
 return <SettingsGroup title="Local model gateway"><SettingsRow label="Base URL" hint="OpenAI-compatible endpoint">{field('baseUrl','Local OpenAI base URL')}</SettingsRow><SettingsRow label="Model">{field('model','Local OpenAI model')}</SettingsRow>{error&&<p role="alert">{error}</p>}</SettingsGroup>;
}
