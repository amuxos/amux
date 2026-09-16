import readline from 'node:readline';

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

export interface RpcMessage {
  jsonrpc?: '2.0';
  id?: number;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string };
}

export type Handler = (msg: RpcMessage) => Promise<unknown> | unknown;

export class RpcPeer {
  private handlers = new Map<string, Handler>();

  constructor(private input: NodeJS.ReadableStream, private output: NodeJS.WritableStream) {}

  on(method: string, handler: Handler): void {
    this.handlers.set(method, handler);
  }

  notify(method: string, params?: unknown): void {
    this.write({ jsonrpc: '2.0', method, params });
  }

  async run(): Promise<void> {
    const rl = readline.createInterface({ input: this.input, crlfDelay: Infinity });
    for await (const line of rl) {
      if (!line.trim()) continue;
      let msg: RpcMessage;
      try {
        msg = JSON.parse(line);
      } catch {
        continue;
      }
      if (!msg.method) continue;
      const handler = this.handlers.get(msg.method);
      if (!handler) {
        if (msg.id !== undefined) this.write({ jsonrpc: '2.0', id: msg.id, error: { code: -32601, message: `method not found: ${msg.method}` } });
        continue;
      }
      try {
        const result = await handler(msg);
        if (msg.id !== undefined) this.write({ jsonrpc: '2.0', id: msg.id, result: result ?? {} });
      } catch (error) {
        if (msg.id !== undefined) this.write({ jsonrpc: '2.0', id: msg.id, error: { code: -32000, message: error instanceof Error ? error.message : String(error) } });
      }
    }
  }

  private write(msg: RpcMessage): void {
    this.output.write(`${JSON.stringify(msg)}\n`);
  }
}
