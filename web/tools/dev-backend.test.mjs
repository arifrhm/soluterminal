// @vitest-environment node
import { test } from 'vitest';
import assert from 'node:assert/strict';
import { devBackend } from './dev-backend.mjs';

test('local preview requires a separate backend, including WebSocket origin', () => {
  for (const target of [undefined, 'http://127.0.0.1:8787', 'http://127.0.0.1:8799',
    'http://127.0.0.1:18787', 'http://127.0.0.1:28787', 'http://127.0.0.1:38787',
    'http://example.com:25000', 'http://127.0.0.1:25000/api', 'http://user:pass@127.0.0.1:25000']) {
    assert.throws(() => devBackend({ SOLU_LOCAL_PREVIEW: '1', SOLU_DEV_BACKEND: target }));
  }
  assert.equal(devBackend({ SOLU_LOCAL_PREVIEW: '1', SOLU_DEV_BACKEND: 'http://127.0.0.1:25001' }),
    'http://127.0.0.1:25001');
});

test('legacy manual development target remains explicit and compatible', () => {
  assert.equal(devBackend({}), 'http://127.0.0.1:8787');
  assert.equal(devBackend({ SOLU_DEV_BACKEND: 'http://127.0.0.1:25001' }), 'http://127.0.0.1:25001');
});
