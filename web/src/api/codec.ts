import { ScalarType, type DescEnum, type DescField, type DescMessage } from "@bufbuild/protobuf";

// The wire codec between the web app's plain JSON-friendly objects and the
// protobuf-es messages the Connect client speaks, driven by the generated
// message descriptors instead of field names:
//
//   - 64-bit integers are decimal strings in the app (a JS number cannot hold
//     them) and bigint in protobuf-es;
//   - bytes are base64 strings in the app and Uint8Array in protobuf-es (a
//     Uint8Array a caller already holds is sent as it is);
//   - enums are their proto names in the app ("ENTRY_KIND_FILE") and numbers
//     in protobuf-es (a request may name one either way);
//   - a oneof stays {case, value}, as protobuf-es has it.
//
// Because the descriptor says what each field is, a new RPC or a new int64,
// bytes or enum field needs no change here, and a field that merely shares a
// name with one of another type (limit is int32 on most lists and int64 on one)
// is encoded as what it is.

const INT64_TYPES = new Set<ScalarType>([
  ScalarType.INT64,
  ScalarType.UINT64,
  ScalarType.SINT64,
  ScalarType.FIXED64,
  ScalarType.SFIXED64
]);

// encodeMessage converts an app request to the init object of a protobuf-es
// message. Fields the message does not have are dropped, as are undefined and
// null ones.
export function encodeMessage(desc: DescMessage, value: unknown): Record<string, unknown> {
  const input = asRecord(value);
  const out: Record<string, unknown> = {};
  if (!input) {
    return out;
  }
  for (const field of desc.fields) {
    if (field.oneof) {
      continue;
    }
    const encoded = encodeField(field, input[field.localName]);
    if (encoded !== undefined) {
      out[field.localName] = encoded;
    }
  }
  for (const oneof of desc.oneofs) {
    const chosen = asRecord(input[oneof.localName]);
    const field = oneof.fields.find((candidate) => candidate.localName === chosen?.case);
    if (chosen && field) {
      const encoded = encodeField(field, chosen.value);
      if (encoded !== undefined) {
        out[oneof.localName] = { case: field.localName, value: encoded };
      }
    }
  }
  return out;
}

// decodeMessage converts a protobuf-es message to the app's object: every
// field it sets, without the $typeName and $unknown bookkeeping.
export function decodeMessage<T = unknown>(desc: DescMessage, value: unknown): T {
  const message = asRecord(value);
  const out: Record<string, unknown> = {};
  if (!message) {
    return out as T;
  }
  for (const field of desc.fields) {
    if (field.oneof) {
      continue;
    }
    const decoded = decodeField(field, message[field.localName]);
    if (decoded !== undefined) {
      out[field.localName] = decoded;
    }
  }
  for (const oneof of desc.oneofs) {
    const chosen = asRecord(message[oneof.localName]);
    if (!chosen) {
      continue;
    }
    const field = oneof.fields.find((candidate) => candidate.localName === chosen.case);
    out[oneof.localName] = field ? { case: field.localName, value: decodeField(field, chosen.value) } : { case: undefined };
  }
  return out as T;
}

function encodeField(field: DescField, value: unknown): unknown {
  if (value === undefined || value === null) {
    return undefined;
  }
  switch (field.fieldKind) {
    case "scalar":
      return encodeScalar(field.scalar, value);
    case "enum":
      return encodeEnum(field.enum, value);
    case "message":
      return encodeMessage(field.message, value);
    case "list":
      return Array.isArray(value) ? value.map((item) => encodeListItem(field, item)) : undefined;
    case "map": {
      const entries = asRecord(value);
      if (!entries) {
        return undefined;
      }
      const out: Record<string, unknown> = {};
      for (const [key, item] of Object.entries(entries)) {
        const encoded = encodeMapValue(field, item);
        if (encoded !== undefined) {
          out[key] = encoded;
        }
      }
      return out;
    }
  }
}

function decodeField(field: DescField, value: unknown): unknown {
  if (value === undefined || value === null) {
    return undefined;
  }
  switch (field.fieldKind) {
    case "scalar":
      return decodeScalar(field.scalar, value);
    case "enum":
      return decodeEnum(field.enum, value);
    case "message":
      return decodeMessage(field.message, value);
    case "list":
      return Array.isArray(value) ? value.map((item) => decodeListItem(field, item)) : [];
    case "map": {
      const entries = asRecord(value) ?? {};
      return Object.fromEntries(Object.entries(entries).map(([key, item]) => [key, decodeMapValue(field, item)]));
    }
  }
}

function encodeListItem(field: DescField & { fieldKind: "list" }, item: unknown) {
  switch (field.listKind) {
    case "scalar":
      return encodeScalar(field.scalar, item);
    case "enum":
      return encodeEnum(field.enum, item);
    case "message":
      return encodeMessage(field.message, item);
  }
}

function decodeListItem(field: DescField & { fieldKind: "list" }, item: unknown) {
  switch (field.listKind) {
    case "scalar":
      return decodeScalar(field.scalar, item);
    case "enum":
      return decodeEnum(field.enum, item);
    case "message":
      return decodeMessage(field.message, item);
  }
}

function encodeMapValue(field: DescField & { fieldKind: "map" }, item: unknown) {
  switch (field.mapKind) {
    case "scalar":
      return encodeScalar(field.scalar, item);
    case "enum":
      return encodeEnum(field.enum, item);
    case "message":
      return encodeMessage(field.message, item);
  }
}

function decodeMapValue(field: DescField & { fieldKind: "map" }, item: unknown) {
  switch (field.mapKind) {
    case "scalar":
      return decodeScalar(field.scalar, item);
    case "enum":
      return decodeEnum(field.enum, item);
    case "message":
      return decodeMessage(field.message, item);
  }
}

function encodeScalar(type: ScalarType, value: unknown): unknown {
  if (INT64_TYPES.has(type)) {
    return toBigInt(value);
  }
  if (type === ScalarType.BYTES) {
    return toBytes(value);
  }
  return value;
}

function decodeScalar(type: ScalarType, value: unknown): unknown {
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (type === ScalarType.BYTES && value instanceof Uint8Array) {
    return bytesToBase64(value);
  }
  return value;
}

function encodeEnum(desc: DescEnum, value: unknown): unknown {
  if (typeof value === "number") {
    return value;
  }
  if (typeof value === "string") {
    const named = desc.values.find((candidate) => candidate.name === value || candidate.localName === value);
    if (named) {
      return named.number;
    }
    throw new Error(`${value} is not a value of ${desc.typeName}`);
  }
  return undefined;
}

// An enum number with no name (a value newer than this build) stays a number.
function decodeEnum(desc: DescEnum, value: unknown): unknown {
  return typeof value === "number" ? (desc.value[value]?.name ?? value) : value;
}

function toBigInt(value: unknown): bigint | undefined {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number") {
    return BigInt(Math.trunc(value));
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    return trimmed ? BigInt(trimmed) : undefined;
  }
  return undefined;
}

function toBytes(value: unknown): Uint8Array | undefined {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (typeof value !== "string") {
    return undefined;
  }
  const binary = globalThis.atob(value);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  const chunkSize = 0x8000;
  for (let index = 0; index < bytes.length; index += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(index, index + chunkSize));
  }
  return globalThis.btoa(binary);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) && !(value instanceof Uint8Array)
    ? (value as Record<string, unknown>)
    : undefined;
}
