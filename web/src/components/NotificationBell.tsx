// NotificationBell renders a bell icon with an unread-count badge in a
// shell header (feature #26, AD6). It polls GetUnreadCount while visible
// and navigates to the notification center on click.

import { useEffect, useState } from 'react';
import { Bell } from '@phosphor-icons/react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { navigate } from '../router';
import { subscribeUnreadChanged } from '../notificationsBus';
import { useI18n } from '../i18n';

interface UnreadResponse {
  response: { code: number; message: string };
  unreadCount: string;
}

export default function NotificationBell({
  path,
  testId,
}: {
  path: string;
  testId: string;
}) {  const { t } = useI18n();

  const api = useApi();
  const { orgId } = useOrg();
  const [unread, setUnread] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const data = await api.get<UnreadResponse>(`${path}/unread-count`, orgId);
        if (!cancelled) setUnread(parseInt(data.unreadCount || '0', 10) || 0);
      } catch {
        // Ignore poll errors; keep the last known count.
      }
    };
    void load();
    const timer = setInterval(() => void load(), 30000);
    // Re-poll immediately when the notifications page marks notifications
    // read (AC9/D5), so the badge never shows a stale count.
    const unsubscribe = subscribeUnreadChanged(() => void load());
    return () => {
      cancelled = true;
      clearInterval(timer);
      unsubscribe();
    };
  }, [api, orgId, path]);

  const target = path.includes('/admin/') ? '/admin/notifications' : '/notifications';

  return (
    <button
      className="notification-bell"
      data-testid={testId}
      onClick={() => navigate(target)}
      aria-label={t('notifications.title')}
    >
      <Bell size={20} weight="duotone" aria-hidden="true" />
      {unread > 0 && <span className="notification-badge">{unread}</span>}
    </button>
  );
}