// ProfilePage is the end-user console "My profile" page: view and update
// the caller's display name and avatar. Username and email are immutable.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';

interface UserProfile {
  userId: string;
  username: string;
  email: string;
  displayName: string;
  avatar: string;
  createdAt: string;
}

export default function ProfilePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [profile, setProfile] = useState<UserProfile | null>(null);
  const [displayName, setDisplayName] = useState('');
  const [avatar, setAvatar] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ profile: UserProfile }>('/api/v1/auth/profile', orgId);
      setProfile(data.profile);
      setDisplayName(data.profile.displayName || '');
      setAvatar(data.profile.avatar || '');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('profile.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // onAvatarFile reads the selected image as a base64 data URL so the
  // avatar is stored in the database, not fetched from a remote URL.
  const onAvatarFile = async (file?: File) => {
    if (!file) return;
    if (!file.type.startsWith('image/')) {
      setError(t('profile.avatarInvalid'));
      return;
    }
    if (file.size > 512 * 1024) {
      setError(t('profile.avatarTooLarge'));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      if (typeof reader.result === 'string') setAvatar(reader.result);
    };
    reader.readAsDataURL(file);
  };

  const save = async () => {
    setSaving(true);
    setError('');
    setSaved(false);
    try {
      const data = await api.patch<{ profile: UserProfile }>('/api/v1/auth/profile', orgId, {
        displayName: displayName.trim(),
        avatar: avatar.trim(),
      });
      setProfile(data.profile);
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('profile.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="page" data-testid="profile-page"><div className="loading">{t('common.loading')}</div></div>;
  }

  return (
    <div className="page" data-testid="profile-page">
      <h1>{t('profile.title')}</h1>
      {error && <ErrorBanner message={error} />}
      {saved && <div className="success" data-testid="profile-saved">{t('profile.saved')}</div>}
      <div className="panel">
        <div className="form">
          <div className="field">
            <label>{t('profile.username')}</label>
            <input value={profile?.username || ''} disabled data-testid="profile-username" />
          </div>
          <div className="field">
            <label>{t('profile.email')}</label>
            <input value={profile?.email || ''} disabled data-testid="profile-email" />
          </div>
          <div className="field">
            <label>{t('profile.displayName')}</label>
            <input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              data-testid="profile-display-name"
            />
          </div>
          <div className="field">
            <label>{t('profile.avatar')}</label>
            {avatar && (
              <img
                src={avatar}
                alt={t('profile.avatar')}
                data-testid="profile-avatar-preview"
                style={{ width: 72, height: 72, borderRadius: '50%', objectFit: 'cover' }}
              />
            )}
            <input
              type="file"
              accept="image/*"
              data-testid="profile-avatar-file"
              onChange={(e) => void onAvatarFile(e.target.files?.[0])}
            />
            {avatar && (
              <button
                type="button"
                className="link danger"
                data-testid="profile-avatar-clear"
                onClick={() => setAvatar('')}
              >
                {t('profile.avatarRemove')}
              </button>
            )}
          </div>
          <button data-testid="profile-save" disabled={saving} onClick={() => void save()}>
            {saving ? t('common.working') : t('profile.save')}
          </button>
        </div>
      </div>
    </div>
  );
}