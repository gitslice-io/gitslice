import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ChangeEvent } from "react";

import { useApi } from "../../api/useApi";
import { AccountAvatar, rememberAvatarVersion } from "../../components/AccountAvatar";
import { SliceNotice, getErrorMessage } from "../../components/slices/SlicePageParts";
import { Button } from "../../components/ui/Button";
import { accountProfileKey } from "../../queries/accounts";

const pictureTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const maxPictureBytes = 5 * 1024 * 1024;

// PictureEditor uploads or removes the account's picture. It saves at once,
// apart from the profile form's Save; without an upload the account shows the
// pixel picture generated from its name.
export function PictureEditor({
  account,
  kind,
  version
}: {
  account: string;
  kind: string;
  version: string;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [problem, setProblem] = useState("");
  const change = useMutation({
    mutationFn: (image: Uint8Array | undefined) => api.setAccountAvatar({ account, image }),
    onSuccess: (updated) => {
      queryClient.setQueryData(accountProfileKey(account), updated);
      rememberAvatarVersion(account, updated.avatarVersion ?? "");
    }
  });

  async function choose(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    // Choosing the same file again should upload it again.
    event.target.value = "";
    if (!file) {
      return;
    }
    if (!pictureTypes.includes(file.type)) {
      setProblem("Choose a PNG, JPEG, GIF or WebP picture.");
      return;
    }
    if (file.size > maxPictureBytes) {
      setProblem("Choose a picture of at most 5 MB.");
      return;
    }
    setProblem("");
    change.mutate(new Uint8Array(await file.arrayBuffer()));
  }

  return (
    <div className="grid gap-1 text-xs font-medium text-slate-600 dark:text-zinc-400">
      Picture
      <div className="flex flex-wrap items-center gap-3">
        <AccountAvatar account={account} kind={kind} size="lg" version={version} />
        <input
          accept={pictureTypes.join(",")}
          aria-label="Picture file"
          className="hidden"
          onChange={(e) => void choose(e)}
          ref={input}
          type="file"
        />
        <Button disabled={change.isPending} onClick={() => input.current?.click()} size="md">
          {change.isPending ? "Saving..." : "Upload picture"}
        </Button>
        {version ? (
          <Button disabled={change.isPending} onClick={() => change.mutate(undefined)} size="md">
            Remove picture
          </Button>
        ) : null}
      </div>
      <p className="font-normal text-slate-500 dark:text-zinc-500">
        {version
          ? "Shown cropped to its center square."
          : "Without a picture of its own, the account shows the pixel picture made from its name."}
      </p>
      {problem || change.isError ? (
        <SliceNotice title="Could not change the picture" tone="error">
          {problem || getErrorMessage(change.error)}
        </SliceNotice>
      ) : null}
    </div>
  );
}
