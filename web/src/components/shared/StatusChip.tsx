import { RunStatusGlyph, taskStatusVisual } from './runStatus';
export function StatusChip({status,label,muted=false}:{status:string;label?:string;muted?:boolean}) {
 return <span className={`service-status-chip is-${status}${muted?' is-muted':''}`}><RunStatusGlyph visual={taskStatusVisual(status)} size={11} colored={false}/>{label??({awaiting_approval:'needs approval',pending:'queued'} as Record<string,string>)[status]??status.replaceAll('_',' ')}</span>;
}
