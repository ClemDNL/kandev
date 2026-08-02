"use client";

import { useCallback } from "react";
import { updateAgentProfileAction } from "@/app/actions/agents";
import { useToast } from "@/components/toast-provider";
import { toAgentProfileOption, type AgentProfileOption } from "@/lib/state/slices/settings/types";
import type { Agent, AgentProfile } from "@/lib/types/http";

/**
 * Immediate-save toggle for the /settings/agents profile list. Disabling
 * (or re-enabling) a profile PATCHes it, then mirrors the result into the
 * settings store so the row and every profile picker reflect the change
 * without a reload.
 */
export function useProfileEnabledToggle(
  savedAgents: Agent[],
  setSettingsAgents: (agents: Agent[]) => void,
  setAgentProfiles: (options: AgentProfileOption[]) => void,
) {
  const { toast } = useToast();

  return useCallback(
    async (profile: AgentProfile, enabled: boolean) => {
      try {
        const updated = await updateAgentProfileAction(profile.id, { enabled });
        const nextAgents = savedAgents.map((agentItem: Agent) =>
          agentItem.id === profile.agentId
            ? {
                ...agentItem,
                profiles: agentItem.profiles.map((p: AgentProfile) =>
                  p.id === updated.id ? updated : p,
                ),
              }
            : agentItem,
        );
        setSettingsAgents(nextAgents);
        setAgentProfiles(
          nextAgents.flatMap((agentItem) =>
            agentItem.profiles.map((agentProfile) => toAgentProfileOption(agentItem, agentProfile)),
          ),
        );
      } catch (error) {
        toast({
          title: "Failed to update profile",
          description: error instanceof Error ? error.message : "Request failed",
          variant: "error",
        });
      }
    },
    [savedAgents, setAgentProfiles, setSettingsAgents, toast],
  );
}
