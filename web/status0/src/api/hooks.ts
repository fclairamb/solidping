import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "./client";

export interface ResourceCheckInfo {
  name?: string;
  type: string;
  status: string;
}

export interface DailyAvailabilityPoint {
  date: string;
  availabilityPct: number;
  status: string;
}

export interface ResponseTimePoint {
  time: string;
  durationP95?: number;
}

export interface ResourceAvailabilityData {
  overallAvailabilityPct?: number;
  dailyAvailability?: DailyAvailabilityPoint[];
  responseTimeData?: ResponseTimePoint[];
}

export interface StatusPageResource {
  uid: string;
  checkUid: string;
  publicName?: string;
  explanation?: string;
  position: number;
  check?: ResourceCheckInfo;
  availability?: ResourceAvailabilityData;
  createdAt?: string;
}

export interface StatusPageSection {
  uid: string;
  name: string;
  slug: string;
  position: number;
  resources?: StatusPageResource[];
  createdAt?: string;
}

export interface StatusPage {
  uid: string;
  name: string;
  slug: string;
  description?: string;
  visibility: string;
  isDefault: boolean;
  enabled: boolean;
  showAvailability: boolean;
  showResponseTime: boolean;
  historyDays: number;
  language?: string;
  sections?: StatusPageSection[];
  createdAt?: string;
}

export function usePublicStatusPage(org: string, slug: string) {
  return useQuery<StatusPage>({
    queryKey: ["public-status-page", org, slug],
    queryFn: () => apiFetch<StatusPage>(`/api/v1/status-pages/${org}/${slug}`),
    refetchInterval: 30_000, // Refresh every 30 seconds
    enabled: !!org && !!slug,
  });
}

export interface VersionInfo {
  version: string;
  commit: string;
  gitTime: string;
}

export function useVersion() {
  return useQuery<VersionInfo>({
    queryKey: ["version"],
    queryFn: () => apiFetch<VersionInfo>("/api/mgmt/version"),
    staleTime: Infinity,
  });
}

export function useDefaultStatusPage(org: string) {
  return useQuery<StatusPage>({
    queryKey: ["public-status-page", org, "__default__"],
    queryFn: () => apiFetch<StatusPage>(`/api/v1/status-pages/${org}`),
    refetchInterval: 30_000,
    enabled: !!org,
  });
}
