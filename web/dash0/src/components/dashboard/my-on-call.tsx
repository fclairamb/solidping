import { useMemo } from "react";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CalendarClock } from "lucide-react";

import { useMembers, useOnCallSchedules, type OnCallSchedule } from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface MyOnCallWidgetProps {
  org: string;
}

// MyOnCallWidget surfaces the schedules the logged-in user is part of.
// Render is conditional: nothing shows if the user is in zero rotations,
// so the widget never adds visual noise for users who aren't paged.
//
// The auth context only carries the user's email — to compare against the
// schedule's userUids we resolve email → UID via the members list, which is
// already cached for the rest of the dashboard.
export function MyOnCallWidget({ org }: MyOnCallWidgetProps) {
  const { t } = useTranslation(["oncall"]);
  const { user } = useAuth();
  const { data: schedules } = useOnCallSchedules(org);
  const { data: membersResp } = useMembers(org);

  const myUID = useMemo<string | null>(() => {
    if (!user) return null;
    const me = (membersResp?.data ?? []).find(
      (m) => m.email.toLowerCase() === user.email.toLowerCase(),
    );
    return me?.userUid ?? null;
  }, [user, membersResp]);

  const mine = useMemo<OnCallSchedule[]>(() => {
    if (!schedules || !myUID) return [];
    return schedules.filter(
      (s) => (s.userUids ?? []).includes(myUID),
    );
  }, [schedules, myUID]);

  if (mine.length === 0) return null;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CalendarClock className="h-4 w-4" />
          {t("oncall:myOnCall.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {mine.map((schedule) => {
          const onNow = schedule.currentlyOnCall?.uid === myUID;
          return (
            <div
              key={schedule.uid}
              className="flex items-center justify-between border rounded p-2"
            >
              <Link
                to="/orgs/$org/on-call/$uid"
                params={{ org, uid: schedule.uid }}
                className="hover:underline font-medium"
              >
                {schedule.name}
              </Link>
              {onNow ? (
                <Badge className="bg-amber-500/10 text-amber-700">
                  {t("oncall:myOnCall.onCallNow")}
                </Badge>
              ) : (
                <span className="text-xs text-muted-foreground">
                  {schedule.currentlyOnCall
                    ? schedule.currentlyOnCall.name
                    : t("oncall:list.noOneOnCall")}
                </span>
              )}
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
