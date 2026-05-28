"use client";

import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { notificationPreferenceOptions } from "@multica/core/notification-preferences/queries";
import { useUpdateNotificationPreferences } from "@multica/core/notification-preferences/mutations";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import type { NotificationGroupKey, NotificationPreferences, Workspace } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Switch } from "@multica/ui/components/ui/switch";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { useT } from "../../i18n";

// Inbox event groups rendered in the per-event toggle list. `system_notifications`
// is a sibling preference key but lives in its own section below.
const INBOX_GROUP_KEYS = [
  "assignments",
  "status_changes",
  "comments",
  "updates",
  "agent_activity",
] as const;
type InboxGroupKey = (typeof INBOX_GROUP_KEYS)[number];

export function NotificationsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const user = useAuthStore((s) => s.user);
  const qc = useQueryClient();
  const { data } = useQuery(notificationPreferenceOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const mutation = useUpdateNotificationPreferences();
  const [botToken, setBotToken] = useState(workspace?.settings.telegram?.bot_token ?? "");
  const [userId, setUserId] = useState(workspace?.settings.telegram?.user_id ?? "");
  const [saving, setSaving] = useState(false);

  const preferences = data?.preferences ?? {};
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner" || currentMember?.role === "admin";
  const hasPartialConfig = useMemo(() => {
    const hasBotToken = botToken.trim().length > 0;
    const hasUserId = userId.trim().length > 0;
    return hasBotToken !== hasUserId;
  }, [botToken, userId]);

  const handleToggle = (key: NotificationGroupKey, enabled: boolean) => {
    const updated: NotificationPreferences = {
      ...preferences,
      [key]: enabled ? "all" : "muted",
    };
    // Remove keys set to "all" (default) to keep the object clean
    if (enabled) {
      delete updated[key];
    }
    mutation.mutate(updated, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.notifications.toast_failed),
        ),
    });
  };

  const systemEnabled = preferences.system_notifications !== "muted";

  const handleTelegramSave = async () => {
    if (!workspace) return;
    if (hasPartialConfig) {
      toast.error(t(($) => $.notifications.telegram.validation_error));
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
      };
    }

    try {
      const updated = await api.updateWorkspace(workspace.id, { settings: nextSettings });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.notifications.telegram.toast_saved));
    } catch (e) {
      toast.error(
        e instanceof Error && e.message
          ? e.message
          : t(($) => $.notifications.telegram.toast_save_failed),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div>
          <h2 className="text-sm font-semibold">{t(($) => $.notifications.title)}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t(($) => $.notifications.description)}
          </p>
        </div>

        <Card>
          <CardContent className="divide-y">
            {INBOX_GROUP_KEYS.map((key: InboxGroupKey) => {
              const enabled = preferences[key] !== "muted";
              return (
                <div
                  key={key}
                  className="flex items-center justify-between py-3 first:pt-0 last:pb-0"
                >
                  <div className="space-y-0.5 pr-4">
                    <p className="text-sm font-medium">{t(($) => $.notifications.groups[key].label)}</p>
                    <p className="text-xs text-muted-foreground">
                      {t(($) => $.notifications.groups[key].description)}
                    </p>
                  </div>
                  <Switch
                    checked={enabled}
                    onCheckedChange={(checked) => handleToggle(key, checked)}
                  />
                </div>
              );
            })}
          </CardContent>
        </Card>
      </section>

      <section className="space-y-4">
        <div>
          <h2 className="text-sm font-semibold">{t(($) => $.notifications.system.title)}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t(($) => $.notifications.system.description)}
          </p>
        </div>

        <Card>
          <CardContent>
            <div className="flex items-center justify-between">
              <div className="space-y-0.5 pr-4">
                <p className="text-sm font-medium">{t(($) => $.notifications.system.label)}</p>
                <p className="text-xs text-muted-foreground">
                  {t(($) => $.notifications.system.hint)}
                </p>
              </div>
              <Switch
                checked={systemEnabled}
                onCheckedChange={(checked) => handleToggle("system_notifications", checked)}
              />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-4">
        <div>
          <h2 className="text-sm font-semibold">{t(($) => $.notifications.telegram.title)}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t(($) => $.notifications.telegram.description)}
          </p>
        </div>

        <Card>
          <CardContent className="space-y-3">
            <div>
              <Label className="text-xs text-muted-foreground">
                {t(($) => $.notifications.telegram.bot_token_label)}
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
                {t(($) => $.notifications.telegram.user_id_label)}
              </Label>
              <Input
                type="text"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
                disabled={!canManageWorkspace}
                className="mt-1"
              />
            </div>

            {hasPartialConfig && (
              <p className="text-xs text-destructive">
                {t(($) => $.notifications.telegram.partial_config_error)}
              </p>
            )}

            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                onClick={handleTelegramSave}
                disabled={!canManageWorkspace || saving || hasPartialConfig}
              >
                {saving
                  ? t(($) => $.notifications.telegram.saving)
                  : t(($) => $.notifications.telegram.save)}
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
