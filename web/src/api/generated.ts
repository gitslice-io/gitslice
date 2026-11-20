import type {
  DescMethodServerStreaming,
  DescMethodUnary,
  DescService,
  Message,
  MessageShape
} from "@bufbuild/protobuf";

import type { AgentService } from "../gen/proto/core/v1/agent_pb";
import type { AuthService } from "../gen/proto/core/v1/auth_pb";
import type { BlobService } from "../gen/proto/core/v1/blob_pb";
import type { ChangesetService, ChangesetStackService } from "../gen/proto/core/v1/changeset_pb";
import type { CheckService } from "../gen/proto/core/v1/check_pb";
import type { FeedbackService } from "../gen/proto/core/v1/feedback_pb";
import type { RepositoryService } from "../gen/proto/core/v1/repository_pb";
import type { SecretService } from "../gen/proto/core/v1/secret_pb";
import type { SliceService } from "../gen/proto/core/v1/slice_pb";
import type { AutomationService } from "../gen/proto/core/v1/automation_pb";

// Types only: the generated services and messages are named here to derive from
// them, not to import them at runtime, so this file adds nothing to the page's
// script (the descriptors live in ./client, loaded on demand by ./lazyClient).
//
// NormalizedMessage<T> and NormalizedRequest<T> model the plain JSON-friendly
// objects ./codec decodes from and encodes to protobuf-es messages.

type Primitive = string | number | boolean | undefined | null;

// What a message looks like to the app once decoded (./codec): 64-bit
// integers and bytes are strings, enums are their names, no $typeName, and
// every field may be absent.
export type NormalizedMessage<T> = {
  [K in keyof T as K extends `$${string}` ? never : K]?: NormalizedValue<T[K]>;
};

type NormalizedValue<V> = V extends bigint
  ? string
  : V extends Uint8Array
    ? string
    : V extends number
      ? `${number}` extends `${V}`
        ? number
        : string // an enum
      : V extends Primitive
        ? V
        : V extends readonly (infer Item)[]
          ? NormalizedValue<Item>[]
          : V extends Message
            ? NormalizedMessage<V>
            : string extends keyof V
              ? V extends Record<string, infer MapItem>
                ? Record<string, NormalizedValue<MapItem>>
                : V
              : V extends object
                ? NormalizedMessage<V>
                : V;

// What a request may hold: 64-bit integers as numbers, bigints or decimal
// strings, bytes as base64 or bytes, an enum by name or number.
export type NormalizedRequest<T> = {
  [K in keyof T as K extends `$${string}` ? never : K]?: RequestValue<T[K]>;
};

type RequestValue<V> = V extends bigint
  ? string | number | bigint
  : V extends Uint8Array
    ? Uint8Array | string
    : V extends number
      ? `${number}` extends `${V}`
        ? number
        : number | string
      : V extends Primitive
        ? V
        : V extends readonly (infer Item)[]
          ? RequestValue<Item>[]
          : V extends Message
            ? NormalizedRequest<V>
            : string extends keyof V
              ? V extends Record<string, infer MapItem>
                ? Record<string, RequestValue<MapItem>>
                : V
              : V extends object
                ? NormalizedRequest<V>
                : V;

type ApiMethod<M> =
  M extends DescMethodUnary<infer I, infer O>
    ? (request: NormalizedRequest<MessageShape<I>>) => Promise<NormalizedMessage<MessageShape<O>>>
    : M extends DescMethodServerStreaming<infer I, infer O>
      ? (
          request: NormalizedRequest<MessageShape<I>>,
          signal: AbortSignal
        ) => AsyncGenerator<NormalizedMessage<MessageShape<O>>>
      : never;

// The unary and server-streaming methods of a service, by name. Client- and
// bidirectional-streaming ones (daemon connections, chunked uploads) are not
// for a browser.
export type ServiceApi<S extends DescService> = {
  [K in keyof S["method"] as ApiMethod<S["method"][K]> extends never ? never : K]: ApiMethod<S["method"][K]>;
};

// Every RPC of the services a browser calls.
export type GeneratedApi = ServiceApi<typeof AuthService> &
  ServiceApi<typeof AgentService> &
  ServiceApi<typeof BlobService> &
  ServiceApi<typeof CheckService> &
  ServiceApi<typeof ChangesetService> &
  ServiceApi<typeof ChangesetStackService> &
  ServiceApi<typeof FeedbackService> &
  ServiceApi<typeof RepositoryService> &
  ServiceApi<typeof SecretService> &
  ServiceApi<typeof SliceService> &
  ServiceApi<typeof AutomationService>;
