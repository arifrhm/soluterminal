import { test as base } from 'playwright/test';
import {buildServiceFixtures,routeServiceFixtures} from '../tools/screens-lib.mjs';
import {installStoragePreferences} from '../tools/e2e-smoke.mjs';
export const test=base.extend({serviceFixtures:[async({context,page},use)=>{const fixtures=buildServiceFixtures();await routeServiceFixtures(context,fixtures);await installStoragePreferences(page,'dark');page.setDefaultTimeout(10000);await use(fixtures)},{auto:true}]});
export {expect} from 'playwright/test';
