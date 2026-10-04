import { useEffect, useMemo, useState } from "react";

export function base64ToBytes(data: string): Uint8Array<ArrayBuffer> {
  const binary = window.atob(data);
  const bytes = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

// useObjectUrl makes a blob: URL for base64 file data, and revokes it when the
// data changes or the component goes away.
export function useObjectUrl(data: string, type: string) {
  const [url, setUrl] = useState("");
  useEffect(() => {
    if (!data) {
      setUrl("");
      return;
    }
    let created = "";
    try {
      created = URL.createObjectURL(new Blob([base64ToBytes(data)], { type }));
    } catch {
      created = "";
    }
    setUrl(created);
    return () => {
      if (created) {
        URL.revokeObjectURL(created);
      }
    };
  }, [data, type]);
  return url;
}

// useBytes decodes base64 file data once.
export function useBytes(data: string) {
  return useMemo(() => {
    try {
      return base64ToBytes(data);
    } catch {
      return null;
    }
  }, [data]);
}
