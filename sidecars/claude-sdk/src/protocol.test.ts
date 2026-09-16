import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { test } from 'node:test';
import { RpcPeer } from './protocol.js';

test('RpcPeer handles request and writes response', async () => {
  const input = new PassThrough();
  const output = new PassThrough();
  const peer = new RpcPeer(input, output);
  peer.on('ping', () => ({ ok: true }));
  const run = peer.run();
  const chunks: Buffer[] = [];
  output.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
  input.write('{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}\n');
  await new Promise((resolve) => setTimeout(resolve, 10));
  input.end();
  await run;
  const line = Buffer.concat(chunks).toString().trim();
  const msg = JSON.parse(line);
  assert.equal(msg.id, 1);
  assert.deepEqual(msg.result, { ok: true });
});
