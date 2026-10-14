import type { ServerDef } from "@/api/client";

/**
 * The one state vocabulary of the panel. The server list, the sidebar rail and
 * the dashboard all read a server's state through these helpers, so the same
 * server never shows up as "running" in one place and "updating" in another.
 */

type Stateful = Pick<ServerDef, "importing" | "updating" | "state">;

/** Derived state: an in-flight import/update outranks the reported state. */
export function serverState(s: Stateful): string {
  if (s.importing) return "importing";
  if (s.updating) return "updating";
  return s.state || "stopped";
}

/** States that are on their way somewhere: buttons stay disabled meanwhile. */
export function isTransitional(state: string): boolean {
  return state === "starting" || state === "stopping" || state === "importing" || state === "updating";
}

/** Badge variant for a derived state. */
export function stateVariant(state: string): "success" | "destructive" | "warning" | "muted" {
  switch (state) {
    case "running":
      return "success";
    case "crashed":
      return "destructive";
    case "starting":
    case "stopping":
    case "importing":
    case "updating":
      return "warning";
    default:
      return "muted";
  }
}

/** Tailwind classes for the small status dot used in lists and the rail. */
export function stateDotClass(state: string): string {
  switch (state) {
    case "running":
      return "bg-success animate-pulse-dot";
    case "crashed":
      return "bg-destructive";
    case "starting":
    case "stopping":
    case "importing":
    case "updating":
      return "bg-warning";
    default:
      return "bg-muted-foreground/50";
  }
}
