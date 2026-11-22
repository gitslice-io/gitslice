/** Extracts a human-readable error message from an unknown thrown value. */
export function getErrorMessage(error: unknown, fallback = "Something went wrong."): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return fallback;
}
