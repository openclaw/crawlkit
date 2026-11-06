export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };
export interface RequestBase { protocol: 'sessionowner/1'; requestId: string; bindingId: string; deadline: string; }
export type Request = RequestBase & (
  { kind: 'inspect' } |
  { kind: 'perform'; expectedGeneration: string; operationId: string; operationVersion: number; target: Record<string, JSONValue>; args: Record<string, JSONValue>; confirmationRef: string | null } |
  { kind: 'recover'; mode: 'new'; expectedGeneration: string; cause: 'rejected_generation' | 'expired_session' | 'authorized_replacement'; rejectedRequestId: string | null; recoveryAuthorizationRef: string } |
  { kind: 'recover'; mode: 'resume'; expectedGeneration: string; claimId: string; recoveryAuthorizationRef: string } |
  { kind: 'cancel'; targetRequestId: string; reason: 'caller_request' | 'deadline' }
);
export const PROTOCOL: 'sessionowner/1';
export const HARD_FRAME_LIMIT: number;
export function decodeJSON(bytes: Uint8Array, cap: number): JSONValue;
export function encodeFrame(value: JSONValue | Uint8Array, cap: number): Uint8Array;
export class SchemaStore { constructor(); register(id: string, bytes: Uint8Array): void; validate(id: string, value: JSONValue): JSONValue; }
export class Registry { constructor(bytes: Uint8Array, expectedSHA256: string, artifacts: Map<string, Uint8Array>); readonly bindingId: string; readonly channelCap: number; parseRequest(bytes: Uint8Array): {request: Request; operation: any}; validateResponse(bytes: Uint8Array, pending: ReturnType<Registry['parseRequest']>): JSONValue; }
export class FrameDecoder { constructor(cap: number); push(chunk: Uint8Array): Uint8Array[]; end(): void; }
export interface AuthenticatedTransport { roundTrip(frame: Uint8Array, options: {signal?: AbortSignal}): Promise<Uint8Array>; }
export class OperationClient { constructor(registry: Registry, transport: AuthenticatedTransport); request(value: Request, options?: {signal?: AbortSignal}): Promise<JSONValue>; cancel(targetRequestId: string, deadline: string): Promise<JSONValue>; }
