import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";

import type { AccountProfile } from "../../api/types";
import { useApi } from "../../api/useApi";
import { SliceNotice, getErrorMessage } from "../../components/slices/SlicePageParts";
import { Button } from "../../components/ui/Button";
import { Input, inputClassName } from "../../components/ui/Input";
import { accountProfileKey } from "../../queries/accounts";
import { PictureEditor } from "./PictureEditor";

// ProfileEditor edits an account's display name, description and website.
export function ProfileEditor({
  account,
  onDone,
  profile
}: {
  account: string;
  onDone(): void;
  profile: AccountProfile;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [displayName, setDisplayName] = useState(profile.displayName ?? "");
  const [description, setDescription] = useState(profile.description ?? "");
  const [website, setWebsite] = useState(profile.website ?? "");
  const save = useMutation({
    mutationFn: () => api.updateAccountProfile({ account, description, displayName, website }),
    onSuccess: (updated) => {
      queryClient.setQueryData(accountProfileKey(account), updated);
      onDone();
    }
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate();
  }

  return (
    <form aria-label="Edit profile" className="mt-4 grid max-w-xl gap-3" onSubmit={submit}>
      <PictureEditor account={account} kind={profile.kind ?? ""} version={profile.avatarVersion ?? ""} />
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Display name
        <Input maxLength={64} onChange={(e) => setDisplayName(e.target.value)} value={displayName} />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Description
        <textarea
          className={inputClassName({ className: "h-auto py-2" })}
          maxLength={280}
          onChange={(e) => setDescription(e.target.value)}
          rows={3}
          value={description}
        />
      </label>
      <label className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
        Website
        <Input onChange={(e) => setWebsite(e.target.value)} placeholder="https://" type="url" value={website} />
      </label>
      {save.isError ? (
        <SliceNotice title="Could not save the profile" tone="error">
          {getErrorMessage(save.error)}
        </SliceNotice>
      ) : null}
      <div className="flex gap-2">
        <Button disabled={save.isPending} size="md" type="submit" variant="primary">
          {save.isPending ? "Saving..." : "Save profile"}
        </Button>
        <Button disabled={save.isPending} onClick={onDone} size="md">
          Cancel
        </Button>
      </div>
    </form>
  );
}
