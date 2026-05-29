"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug } from "lucide-react";
import { useMemo, useState } from "react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@multica/ui/components/ui/empty";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { useT } from "../../i18n";

export function IntegrationsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const user = useAuthStore((s) => s.user);
  const qc = useQueryClient();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const [botToken, setBotToken] = useState(workspace?.settings.telegram?.bot_token ?? "");
  const [userId, setUserId] = useState(workspace?.settings.telegram?.user_id ?? "");
  const [notifyReactions, setNotifyReactions] = useState(
    workspace?.settings.telegram?.notify_reactions !== false,
  );
  const [saving, setSaving] = useState(false);

  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner" || currentMember?.role === "admin";
  const hasPartialConfig = useMemo(() => {
    const hasBotToken = botToken.trim().length > 0;
    const hasUserId = userId.trim().length > 0;
    return hasBotToken !== hasUserId;
  }, [botToken, userId]);

  const handleTelegramSave = async () => {
    if (!workspace) return;
    if (hasPartialConfig) {
      toast.error(t(($) => $.integrations.telegram.validation_error));
      return;
    }

    setSaving(true);
    const nextSettings = { ...(workspace.settings ?? {}) } as Workspace["settings"];
    if (!botToken.trim() && !userId.trim()) {
      delete nextSettings.telegram;
    } else {
      nextSettings.telegram = {
        bot_token: botToken.trim(),
        user_id: userId.trim(),
        notify_reactions: notifyReactions,
      };
    }

    try {
      const updated = await api.updateWorkspace(workspace.id, { settings: nextSettings });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.integrations.telegram.toast_saved));
    } catch (e) {
      toast.error(
        e instanceof Error && e.message
          ? e.message
          : t(($) => $.integrations.telegram.toast_save_failed),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <h2 className="text-sm font-semibold">{t(($) => $.integrations.section_title)}</h2>

        <Card>
          <CardContent className="space-y-3 pt-6">
            <div>
              <h3 className="text-sm font-semibold">{t(($) => $.integrations.telegram.title)}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {t(($) => $.integrations.telegram.description)}
              </p>
            </div>

            <div>
              <Label className="text-xs text-muted-foreground">
                {t(($) => $.integrations.telegram.bot_token_label)}
              </Label>
              <Input
                type="password"
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                disabled={!canManageWorkspace}
                className="mt-1"
                autoComplete="off"
              />
            </div>

            <div>
              <Label className="text-xs text-muted-foreground">
                {t(($) => $.integrations.telegram.user_id_label)}
              </Label>
              <Input
                type="text"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
                disabled={!canManageWorkspace}
                className="mt-1"
              />
            </div>

            <div className="flex items-center justify-between gap-4">
              <div className="space-y-0.5 pr-4">
                <p className="text-sm font-medium">
                  {t(($) => $.integrations.telegram.notify_reactions_label)}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t(($) => $.integrations.telegram.notify_reactions_hint)}
                </p>
              </div>
              <Switch
                checked={notifyReactions}
                onCheckedChange={setNotifyReactions}
                disabled={!canManageWorkspace}
              />
            </div>

            {hasPartialConfig && (
              <p className="text-xs text-destructive">
                {t(($) => $.integrations.telegram.partial_config_error)}
              </p>
            )}

            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                onClick={handleTelegramSave}
                disabled={!canManageWorkspace || saving || hasPartialConfig}
              >
                {saving
                  ? t(($) => $.integrations.telegram.saving)
                  : t(($) => $.integrations.telegram.save)}
              </Button>
            </div>
          </CardContent>
        </Card>

        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Plug className="h-4 w-4" />
            </EmptyMedia>
            <EmptyTitle>{t(($) => $.integrations.empty_title)}</EmptyTitle>
            <EmptyDescription>
              {t(($) => $.integrations.empty_description)}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.integrations.manage_hint)}
            </p>
          </EmptyContent>
        </Empty>
      </section>
    </div>
  );
}
