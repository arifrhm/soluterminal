import { describe, expect, it } from 'vitest';
import type { SystemStatus } from '../../api';
import { ageLabel, blockingLayerLine, cleanupScopeLabel, controllerLine, deltaLabel, diskRows, fmtBytes, fmtMB, fmtPctOf, growthStateLabel, guestOnlyExplanation, hostMemoryLabel, hostOffenderLine, kpiTiles, levelVisual, pressureRows, recentRollback, storageProducerRows, topProcesses, untilLabel, windowsHostLine } from './systemModel';

function fixture(): SystemStatus {
  return {
    level:{overall:'warn',reason:'disk low',reasons:[],areas:{daemonMemory:'critical',hostMemory:'warn',disk:'warn',engine:'ok'}},
    daemon:{memGuardMB:6144},host:{memAvailableMB:4096,memTotalMB:8192,memFloorMB:2048},
    footprint:{totalRssMB:2048,daemonRssMB:512,panesRssMB:1024,workersRssMB:512,paneCount:2,workerCount:1,procs:9,cpuPct:2,complete:false},
    disk:{freeMB:4096,totalMB:10000,freeBytes:4096*1024*1024,totalBytes:10000*1024*1024,usedBytes:5904*1024*1024,floorMB:2048,measuring:false,breakdown:[
      {name:'other_soluterminal_data',category:'other_soluterminal_data',label:'Other SoluTerminal data',bytes:700,apparentBytes:700,complete:true,measured:true,protection:'read-only',cleanup:'none'},
      {name:'shared_go_build',category:'shared_go_build',label:'Shared Go build cache',bytes:300,apparentBytes:300,complete:true,measured:true,protection:'managed',cleanup:'go_cache_janitor'},
      {name:'external_user_cache',category:'external_user_cache',label:'External user cache',bytes:5000,apparentBytes:5000,complete:true,measured:true,protection:'read-only',cleanup:'none'},
    ]},
    engine:{available:true,decide:2,queue:{running:2,cap:4,heavyRunning:1,heavyCap:2}},
    panes:[{id:'p1',title:'One',state:'busy',status:'running',procs:2,rssMB:800,cpuPct:3},{id:'p2',title:'Two',state:'idle',status:'running',procs:1,rssMB:200,cpuPct:1}],
    workers:[{taskId:'t1',taskTitle:'Task',stage:'execute',procs:3,rssMB:600,cpuPct:2}],
  } as unknown as SystemStatus;
}

describe('systemModel formatters',()=>{
  it('keeps unknown honest and switches to GB at 1024',()=>{expect(fmtMB(null)).toBe('—');expect(fmtMB(1024)).toContain('GB');expect(fmtBytes(null)).toBe('—');expect(fmtBytes(1024)).toBe('1.00 KB');expect(fmtPctOf(null,3)).toBe('—');expect(fmtPctOf(1,4)).toBe('25%');});
  it('formats ages, countdowns and level visuals',()=>{const now=Date.parse('2026-01-01T00:01:00Z');expect(ageLabel(now-30_000,now)).toBe('30s ago');expect(untilLabel(now+30_000,now)).toBe('in 30s');expect(levelVisual('critical')).toBe('error');expect(levelVisual('unknown')).toBe('neutral');});
});

describe('systemModel projections',()=>{
  it('keeps missing controller advice honest',()=>{
    const status=fixture();
    status.engine.queue.heavyController={mode:'governed',capEff:4,heavyMax:5,reason:'floor',floorActive:true} as NonNullable<typeof status.engine.queue.heavyController>;
    expect(controllerLine(status.engine.queue)).toContain('configured minimum 4');
    expect(controllerLine(status.engine.queue)).not.toContain('undefined');
    expect(controllerLine(status.engine.queue)).not.toContain('advisory');
    status.engine.queue.heavyController.wouldCap=1;
    expect(controllerLine(status.engine.queue)).toContain('advisory 1');
  });

	it('names WSL and Windows memory provenance and controller rollback states',()=>{
	  const status=fixture();status.host.provenance='wsl';status.host.windows={status:'stale',freeMB:3072,ageSec:120,provenance:'windows'};
	  status.host.pressure={at:'2026-08-25T00:00:00Z',memPSISome10:null,memPSIFull10:0,cpuPSISome60:2,ioPSIFull60:1,swapOutPerSec:0,loadAvg1:3,nproc:24,provenance:'wsl'};
	  status.engine.queue.heavyController={mode:'governed',phase:'governed',capEff:3,heavyMax:5,staticCap:2,staticSource:'adaptive',governedMax:4,reason:'rollback hold',frozen:false,pinned:false,greenSec:0,redSec:0,cap4Eligible:false,shadowObservedSec:14400,shadowRequiredSec:14400,governedEvidenceSec:0,governedRequiredSec:7200,rollbacks:[{at:'2026-08-25T00:00:00Z',from:4,to:3,reason:'swap',phase:'governed'}]};
	  expect(hostMemoryLabel(status)).toBe('WSL VM memory');expect(windowsHostLine(status)).toContain('stale · 120s old');expect(pressureRows(status)[0][1]).toContain('unknown');expect(controllerLine(status.engine.queue)).toContain('effective 3');status.engine.queue.heavyController.floorActive=true;status.engine.queue.heavyController.floorCap=3;status.engine.queue.heavyController.wouldCap=1;expect(controllerLine(status.engine.queue)).toContain('configured minimum 3 (advisory 1)');expect(recentRollback(status.engine.queue,Date.parse('2026-08-25T01:00:00Z'))?.to).toBe(3);
	  status.host.windows={status:'timeout',sampledAt:'2026-08-25T00:00:00Z',provenance:'windows'};
	  expect(windowsHostLine(status,Date.parse('2026-08-25T00:01:00Z'))).toContain('timeout · 60s old');
	  expect(recentRollback(status.engine.queue,Date.parse('2026-08-24T23:59:00Z'))).toBeNull();
	});
  it('builds bounded KPI fractions and incomplete prefix',()=>{const tiles=kpiTiles(fixture());expect(tiles).toHaveLength(4);expect(tiles[0].value).toMatch(/^≥/);expect(tiles[1].meter).toBe(.5);expect(tiles[1].floor).toBe(.25);});
  it('makes the Footprint accent and meter describe total RSS',()=>{
    const footprint=kpiTiles(fixture())[0];
    expect(footprint.level).toBe('warn');
    expect(footprint.meter).toBe(.25);
  });
  it('distinguishes WSL guest and Windows host disk tiles',()=>{
    const status=fixture();
    status.disk.layer='guest';status.disk.blockingLayer='host';status.disk.anyLow=true;
    status.disk.host={available:true,state:'ok',detail:'',volume:'C:',distro:'Ubuntu',freeMB:41000,totalMB:2050000,floorMB:61500,floorPct:3,hysteresisMB:4096,low:true,vhdxPath:'~\\ext4.vhdx',vhdxPhysicalMB:566000,vhdxLogicalMB:1048576,sparse:false,sampledAt:'2026-01-01T00:00:00Z',runbook:'docs/RUNBOOK-wsl-host-disk.md',unexplainedLossMB:51095,vhdxGrowthMB:0,lossWindowMin:43,offender:{label:'%TEMP%\\wsl-crashes',text:'51.0 GB of WSL crash dumps (10 .dmp files) in %TEMP%\\wsl-crashes',mb:51200,dumps:10,files:10,complete:true,path:'~\\AppData\\Local\\Temp\\wsl-crashes'}};
    status.level.areas.hostDisk='critical';
    const tiles=kpiTiles(status);expect(tiles.find(t=>t.key==='disk')?.label).toBe('Disk (WSL guest)');
    const host=tiles.find(t=>t.key==='hostDisk');expect(host?.value).toContain('C:');expect(host?.sub).toContain('not sparse');expect(host?.sub).toContain('51.0 GB of WSL crash dumps');expect(host?.level).toBe('critical');
    expect(hostOffenderLine(status.disk.host)).toContain('unexplained host loss');
    expect(blockingLayerLine(status)).toContain('delete it manually');
    status.disk.blockingLayer='both';expect(blockingLayerLine(status)).toContain('Both layers');expect(blockingLayerLine(status)).toContain('51.0 GB of WSL crash dumps');
    status.disk.host.offender=null;status.disk.host.unexplainedLossMB=null;status.disk.blockingLayer='host';expect(blockingLayerLine(status)).toContain('WSL guest still has');
    status.disk.blockingLayer='guest';expect(blockingLayerLine(status)).toContain('Low disk on ~/.solu');
    status.disk.blockingLayer='none';expect(blockingLayerLine(status)).toBeNull();
  });
  it('keeps native disk projection unchanged and exposes unavailable host state',()=>{
    const status=fixture();expect(kpiTiles(status).find(t=>t.key==='disk')?.label).toBe('Disk');expect(kpiTiles(status).find(t=>t.key==='hostDisk')).toBeUndefined();
    status.disk.host={available:false,state:'timeout',detail:'deadline',volume:'',distro:'Ubuntu',freeMB:null,totalMB:null,floorMB:null,floorPct:3,hysteresisMB:4096,low:false,vhdxPath:'',vhdxPhysicalMB:null,vhdxLogicalMB:null,sparse:null,sampledAt:null,runbook:'docs/RUNBOOK-wsl-host-disk.md'};
    const host=kpiTiles(status).find(t=>t.key==='hostDisk');expect(host?.value).toBe('Unavailable');expect(host?.sub).toContain('timeout');
  });
	  it('renders the native guest-low blocking banner without a host sample',()=>{
	    const status=fixture();status.disk.host=null;status.disk.layer='local';status.disk.low=true;status.disk.anyLow=true;status.disk.blockingLayer='guest';
	    expect(blockingLayerLine(status)).toContain('Low disk on ~/.solu');
	  });
  it('sorts processes and aggregates the rest',()=>{const rows=topProcesses(fixture(),2);expect(rows.map(r=>r.name)).toEqual(['One','Task','1 other processes']);expect(rows[2].rssMB).toBe(200);});
  it('sorts disk rows and keeps shares bounded to SoluTerminal data',()=>{
    const rows=diskRows(fixture().disk.breakdown);
    expect(rows.map((row)=>row.name)).toEqual(['external_user_cache','other_soluterminal_data','shared_go_build']);
    expect(rows.every((row)=>row.share>=0&&row.share<=1)).toBe(true);
    const owned=rows.filter((row)=>row.category!=='external_user_cache');
    expect(owned.reduce((sum,row)=>sum+row.share,0)).toBeCloseTo(1,5);
    expect(rows.find((row)=>row.category==='external_user_cache')?.share).toBe(0);
  });
  it('cannot inflate the ~/.solu total with nested or external categories',()=>{
    const rows=diskRows([
      {name:'other_soluterminal_data',category:'other_soluterminal_data',bytes:100,complete:true,measured:true},
      {name:'workflow_worktrees',category:'workflow_worktrees',bytes:100,complete:true,measured:true},
      {name:'external_user_cache',category:'external_user_cache',bytes:900,complete:true,measured:true},
    ]);
    const owned=rows.filter((row)=>row.category!=='external_user_cache');
    expect(owned.reduce((sum,row)=>sum+row.share,0)).toBeCloseTo(1,5);
    expect(owned.find((row)=>row.category==='workflow_worktrees')?.share).toBeCloseTo(.5,5);
  });
  it('projects producer rows with policy, growth and allocated bytes',()=>{
    const status=fixture();
    status.disk.breakdown![0].allocatedBytes=200;
    status.disk.growth={day:{window:'24h',available:true,continuity:'ok',deltas:[{category:'other_soluterminal_data',label:'Other SoluTerminal data',baselineBytes:600,latestBytes:700,deltaBytes:100,noise:false}]},week:{window:'168h',available:false,continuity:'reboot',deltas:[]}};
    const rows=storageProducerRows(status);
    const other=rows.find((row)=>row.category==='other_soluterminal_data');
    expect(other?.allocatedBytes).toBe(200);
    expect(other?.managed).toBe(false);
    expect(other?.dayDelta?.deltaBytes).toBe(100);
    expect(other?.weekDelta).toBeNull();
    const go=rows.find((row)=>row.category==='shared_go_build');
    expect(go?.managed).toBe(true);
    expect(go?.cleanup).toBe('go_cache_janitor');
  });
  it('keeps legacy caches read-only beside registry-owned generated caches',()=>{
    const status=fixture();
    status.disk.breakdown=[
      {name:'shared_go_build',category:'shared_go_build',label:'Legacy or external Go build cache',bytes:300,complete:true,measured:true,protection:'read-only',cleanup:'none'},
      {name:'managed_go_build',category:'managed_go_build',label:'Owned Go build cache',bytes:200,complete:true,measured:true,protection:'managed',cleanup:'generated_storage_janitor'},
      {name:'unowned_soluterminal_cache',category:'unowned_soluterminal_cache',label:'Other or unverified SoluTerminal cache',bytes:100,complete:true,measured:true,protection:'read-only',cleanup:'none'},
    ];
    const rows=storageProducerRows(status);
    expect(rows.find(row=>row.category==='shared_go_build')).toMatchObject({managed:false,cleanup:'none'});
    expect(rows.find(row=>row.category==='managed_go_build')).toMatchObject({managed:true,cleanup:'generated_storage_janitor'});
    expect(rows.find(row=>row.category==='unowned_soluterminal_cache')).toMatchObject({managed:false,cleanup:'none'});
  });
  it('formats positive, negative and noisy deltas and continuity states',()=>{
    expect(deltaLabel({category:'c',label:'c',baselineBytes:0,latestBytes:10,deltaBytes:10,noise:false})).toContain('+');
    expect(deltaLabel({category:'c',label:'c',baselineBytes:10,latestBytes:0,deltaBytes:-10,noise:false})).toContain('−');
    expect(deltaLabel({category:'c',label:'c',baselineBytes:10,latestBytes:11,deltaBytes:1,noise:true})).toContain('noise');
    expect(deltaLabel(null)).toBe('—');
    expect(growthStateLabel({window:'24h',available:false,continuity:'reboot'})).toBe('reboot boundary');
    expect(growthStateLabel({window:'24h',available:false,continuity:'stale'})).toBe('stale');
    expect(growthStateLabel({window:'24h',available:true,continuity:'ok'})).toBe('measured');
  });
  it('derives the guest-only Windows-maintenance message without a reclaim promise',()=>{
    const status=fixture();
    status.disk.cleanup={complete:true,previewOnly:false,scope:'0 eligible under current workflow GC policy',operations:[{at:'2026-01-01T00:00:00Z',kind:'worktree_gc',layer:'guest',executed:true,dryRun:false,complete:true,verified:false}],state:'guest_only_pending_host_reclaim',message:'Linux space freed; Windows image still allocated; maintenance needed'};
    const explanation=guestOnlyExplanation(status);
    expect(explanation).toContain('Linux space freed; Windows image still allocated; maintenance needed');
    expect(explanation).toContain('No compaction savings are guaranteed');
    expect(cleanupScopeLabel(status)).toContain('0 eligible under current workflow GC policy');
    status.disk.cleanup.previewOnly=true;
    expect(cleanupScopeLabel(status)).toContain('preview only');
    status.disk.cleanup.state=undefined;status.disk.cleanup.message=undefined;
    expect(guestOnlyExplanation(status)).toBeNull();
  });
  it('shows the guest KPI with exact used and free bytes',()=>{
    const status=fixture();
    const disk=kpiTiles(status).find((tile)=>tile.key==='disk');
    expect(disk?.value).toContain('4.00 GB');
    expect(disk?.sub).toContain('used');
  });
});
