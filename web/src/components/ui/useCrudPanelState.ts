import { useCallback, useState } from "react";

import { getErrorMessage } from "../../lib/errors";

export interface CrudPanelState<TItem> {
  editing: string | null;
  deleting: TItem | null;
  error: string;
  startEditing: (id: string | null) => void;
  startNew: () => void;
  cancelEditing: () => void;
  requestDelete: (item: TItem) => void;
  cancelDelete: () => void;
  clearError: () => void;
  setError: (message: string) => void;
  handleError: (error: unknown) => void;
  completeAction: () => void;
}

/**
 * Shared state hook for inline create / edit / delete settings panels.
 * Manages which item (`"new"` or item id) is being edited, which item is
 * awaiting delete confirmation, and panel-level error messages.
 */
export function useCrudPanelState<TItem>(): CrudPanelState<TItem> {
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<TItem | null>(null);
  const [error, setError] = useState("");

  const startEditing = useCallback((id: string | null) => {
    setError("");
    setEditing(id);
  }, []);

  const startNew = useCallback(() => {
    setError("");
    setEditing("new");
  }, []);

  const cancelEditing = useCallback(() => {
    setError("");
    setEditing(null);
  }, []);

  const requestDelete = useCallback((item: TItem) => {
    setError("");
    setDeleting(item);
  }, []);

  const cancelDelete = useCallback(() => {
    setDeleting(null);
  }, []);

  const clearError = useCallback(() => {
    setError("");
  }, []);

  const handleError = useCallback((err: unknown) => {
    setError(getErrorMessage(err));
  }, []);

  const completeAction = useCallback(() => {
    setEditing(null);
    setDeleting(null);
    setError("");
  }, []);

  return {
    editing,
    deleting,
    error,
    startEditing,
    startNew,
    cancelEditing,
    requestDelete,
    cancelDelete,
    clearError,
    setError,
    handleError,
    completeAction
  };
}
