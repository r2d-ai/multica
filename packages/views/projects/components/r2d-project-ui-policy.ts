import type { R2DProjectCapabilities } from "@multica/core/projects/r2d-capabilities";

export type R2DProjectDetailMode =
  | "full"
  | "safe-manage"
  | "safe-contribute"
  | "safe-readonly";

// Deliberately capability-driven. The frontend does not rank viewer/member/
// manager itself; r2dauth remains the sole role resolver.
export function r2dProjectDetailMode(caps: R2DProjectCapabilities): R2DProjectDetailMode {
  if (caps.manage && caps.view_resources) return "full";
  if (caps.manage) return "safe-manage";
  if (caps.contribute) return "safe-contribute";
  return "safe-readonly";
}
