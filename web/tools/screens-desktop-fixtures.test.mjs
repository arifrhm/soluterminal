import { describe, it, expect } from 'vitest';
import { createServer } from './screens-lib.mjs';

function fixture(method, url, state = {}) {
 const server = createServer('/tmp/unused-desktop-fixture-dist', state);
 const handler = server.listeners('request')[0];
 return new Promise((resolve, reject) => {
  let status;
  const response = {
   writeHead(value) { status = value; return response; },
   end(body = '') { try { resolve({ status, body: body ? JSON.parse(String(body)) : null }); } catch(error) { reject(error); } },
  };
  handler({method, url}, response);
 });
}

describe('desktop shell ambient fixtures', () => {
 it('returns the typed unconfigured cloud status for the desktop poller', async () => {
  expect(await fixture('GET','/api/cloud/status')).toEqual({status:200,body:{configured:false,hasCreds:false,credsInvalid:false,claimCorrupt:false,relayState:'unconfigured',pairing:{active:false}}});
 });
 it('answers the mount-time Claude recheck without invoking a CLI', async () => {
  expect(await fixture('POST','/api/setup/claude/recheck')).toEqual({status:200,body:{claudeChecked:true,claudeInstalled:true,claudeVersion:'fixture',claudeAuthed:true}});
 });
 it('supports explicit missing-installation fixtures', async () => {
  const status={claudeChecked:true,claudeInstalled:false,claudeVersion:'',claudeAuthed:false};
  expect((await fixture('POST','/api/setup/claude/recheck',{claudeBootstrapStatus:status})).body).toEqual(status);
 });
 it('keeps unknown desktop API calls and unsupported mutations visibly failing', async () => {
  expect((await fixture('GET','/api/cloud/unknown')).status).toBe(501);
  expect((await fixture('GET','/api/setup/claude/recheck')).status).toBe(501);
  expect((await fixture('POST','/api/cloud/pair/start')).status).toBe(405);
  expect((await fixture('POST','/api/setup/claude/install')).status).toBe(405);
 });
});
