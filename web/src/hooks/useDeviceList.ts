import { useQuery } from "@tanstack/react-query";
import { deviceServiceClient } from "@/connect";
import type { Device } from "@/types/proto/api/v1/po33_device_service_pb";

// useDeviceList fetches the caller's
// bound PO-33 devices. The list is
// the SPA's "My devices" page data
// plus the dropdown the push dialog
// uses. The query key is stable across
// re-renders; the SPA invalidates
// it on revoke.
export const deviceKeys = {
  all: ["po33Devices"] as const,
  list: () => [...deviceKeys.all, "list"] as const,
};

const EMPTY_LIST: Device[] = [];

export function useDeviceList(enabled = true) {
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: deviceKeys.list(),
    queryFn: async () => {
      const response = await deviceServiceClient.listDevices({});
      return response.devices;
    },
    staleTime: 30_000,
    enabled,
  });
  return {
    devices: data ?? EMPTY_LIST,
    isLoading,
    error,
    refetch,
  };
}
