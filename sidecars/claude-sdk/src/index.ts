#!/usr/bin/env node
import { RpcPeer } from './protocol.js';
import { ClaudeBridge } from './claude.js';

const peer = new RpcPeer(process.stdin, process.stdout);
const bridge = new ClaudeBridge(peer);

peer.on('initialize', () => bridge.initialize());
peer.on('session.start', (msg) => bridge.startSession((msg.params ?? {}) as Parameters<typeof bridge.startSession>[0]));
peer.on('turn.start', (msg) => bridge.startTurn((msg.params ?? {}) as { text?: string }));
peer.on('interrupt', () => bridge.interrupt());
peer.on('permission.respond', (msg) => bridge.respondPermission((msg.params ?? {}) as { permission_id?: string; option_id?: string }));
peer.on('shutdown', () => bridge.shutdown());

peer.run().catch((error) => {
  console.error(error instanceof Error ? error.stack ?? error.message : String(error));
  process.exit(1);
});
