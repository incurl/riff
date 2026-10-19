import { useQuery } from "@tanstack/react-query";
import { type PushStatus, type PushStatusState, pollPushStatus } from "@/lib/sketch-push";

// usePushStatus polls the SPA's
// getPushStatus Connect RPC every 2s
// while the dialog is open. The
// query is disabled when the push
// id is empty (initial state, before
// the user clicks Push) and the
// query key is stable across the
// same (device, push) pair. A delivered
// or failed state is the terminal
// state; React Query's
// `refetchInterval` callback
// short-circuits the poll at that
// point so the dialog does not
// hammer the server.

export const pushKeys = {
  all: ["po33Pushes"] as const,
  status: (deviceName: string, pushId: string) => [...pushKeys.all, "status", deviceName, pushId] as const,
};

const TERMINAL_STATES: ReadonlySet<PushStatusState> = new Set(["delivered", "failed", "expired"]);

type UsePushStatusOptions = {
  readonly enabled?: boolean;
};

export function usePushStatus(deviceName: string, pushId: string, options: UsePushStatusOptions = {}) {
  const { enabled = true } = options;
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: pushKeys.status(deviceName, pushId),
    queryFn: async (): Promise<PushStatus> => {
      return pollPushStatus(deviceName, pushId);
    },
    enabled: enabled && Boolean(deviceName) && Boolean(pushId),
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      if (state && TERMINAL_STATES.has(state)) {
        return false;
      }
      return 2000;
    },
    staleTime: 1000,
  });
  return { status: data, isLoading, error, refetch };
}
