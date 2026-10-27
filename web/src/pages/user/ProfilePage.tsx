// ProfilePage is the end-user console "My profile" page. It shows a
// hero card with the real avatar (uploaded and stored inline in the
// database) plus the immutable identity, and lets the user edit the
// display name and avatar.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Camera, EnvelopeSimple, IdentificationBadge, UserCircle } from '@phosphor-icons/react';
import { Avatar, ErrorBanner } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useRealm } from '../../surface';
import { useI18n } from '../../i18n';
import { profilePath, useProfile, type UserProfile } from '../../profile';

export default function ProfilePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const realm = useRealm();
  const path = profilePath(realm);
  const { setProfile: applyProfile } = useProfile();
  const [profile, setProfile] = useState<UserProfile | null>(null);
  const [displayName, setDisplayName] = useState('');
  const [avatar, setAvatar] = useState('');
  // Email change: the new address must be verified with a code sent to it.
  const [email, setEmail] = useState('');
  const [emailCode, setEmailCode] = useState('');
  const [codeSending, setCodeSending] = useState(false);
  const [codeSent, setCodeSent] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const fileInput = useRef<HTMLInputElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ profile: UserProfile }>(path, orgId);
      setProfile(data.profile);
      setDisplayName(data.profile.displayName || '');
      setAvatar(data.profile.avatar || '');
      setEmail(data.profile.email || '');
      applyProfile(data.profile);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('profile.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, path, t, applyProfile]);

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
      const data = await api.patch<{ profile: UserProfile }>(path, orgId, {
        displayName: displayName.trim(),
        avatar,
        email: email.trim(),
        emailCode: emailCode.trim(),
      });
      setProfile(data.profile);
      setAvatar(data.profile.avatar || '');
      setEmail(data.profile.email || '');
      setEmailCode('');
      setCodeSent(false);
      applyProfile(data.profile);
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('profile.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  // The verification-code endpoint is served on both surfaces; the admin
  // console must call its own prefix (the realm guard rejects the other).
  const emailCodePath =
    realm === 'admin' ? '/api/v1/admin/auth/email-code' : '/api/v1/auth/email-code';

  // sendEmailCode asks the platform to mail a code to the new address.
  const sendEmailCode = async () => {
    if (!email.trim()) return;
    setCodeSending(true);
    setError('');
    setSaved(false);
    try {
      await api.post(emailCodePath, orgId, {
        email: email.trim(),
        purpose: 'change_email',
      });
      setCodeSent(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('profile.codeFailed'));
    } finally {
      setCodeSending(false);
    }
  };

  // emailDirty is true when the field differs from the stored address.
  const emailDirty = email.trim() !== (profile?.email || '') && email.trim() !== '';

  if (loading) {
    return (
      <div className="page" data-testid="profile-page">
        <div className="loading">{t('common.loading')}</div>
      </div>
    );
  }

  const shownName = displayName.trim() || profile?.username || '';

  return (
    <div className="page profile-page" data-testid="profile-page">
      <h1>{t('profile.title')}</h1>
      {error && <ErrorBanner message={error} />}
      {saved && (
        <div className="success" data-testid="profile-saved">
          {t('profile.saved')}
        </div>
      )}

      <div className="panel profile-hero">
        <div className="profile-avatar-wrap">
          <Avatar avatar={avatar} name={shownName} size={96} testId="profile-avatar-preview" />
          <button
            type="button"
            className="profile-avatar-edit"
            data-testid="profile-avatar-pick"
            title={t('profile.changeAvatar')}
            aria-label={t('profile.changeAvatar')}
            onClick={() => fileInput.current?.click()}
          >
            <Camera size={16} weight="fill" />
          </button>
          <input
            ref={fileInput}
            type="file"
            accept="image/*"
            className="visually-hidden"
            data-testid="profile-avatar-file"
            onChange={(e) => void onAvatarFile(e.target.files?.[0])}
          />
        </div>

        <div className="profile-hero-meta">
          <div className="profile-hero-name">{shownName}</div>
          <div className="profile-hero-sub">@{profile?.username}</div>
          <div className="profile-chips">
            <span className="profile-chip">
              <IdentificationBadge size={14} />
              {t('profile.username')}
            </span>
            <span className="profile-chip">
              <EnvelopeSimple size={14} />
              {profile?.email || t('profile.noEmail')}
            </span>
          </div>
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
      </div>

      <div className="panel profile-form-panel">
        <div className="profile-form-head">
          <UserCircle size={18} weight="duotone" />
          <span>{t('profile.editTitle')}</span>
        </div>
        <div className="form-grid">
          <div className="form-field full">
            <label htmlFor="profile-username">{t('profile.username')}</label>
            <input id="profile-username" value={profile?.username || ''} disabled data-testid="profile-username" />
            <div className="field-hint">{t('profile.immutableHint')}</div>
          </div>
          <div className="form-field full">
            <label htmlFor="profile-email">{t('profile.email')}</label>
            <div className="toolbar">
              <input
                id="profile-email"
                type="email"
                value={email}
                onChange={(e) => {
                  setEmail(e.target.value);
                  setCodeSent(false);
                }}
                data-testid="profile-email"
                style={{ flex: 1 }}
              />
              <button
                type="button"
                className="secondary"
                data-testid="profile-email-send-code"
                disabled={codeSending || !emailDirty}
                onClick={() => void sendEmailCode()}
              >
                {codeSending ? t('common.working') : t('profile.sendCode')}
              </button>
            </div>
            <div className="field-hint">{t('profile.emailHint')}</div>
          </div>
          {emailDirty && (
            <div className="form-field full">
              <label htmlFor="profile-email-code">{t('profile.emailCode')}</label>
              <input
                id="profile-email-code"
                value={emailCode}
                onChange={(e) => setEmailCode(e.target.value)}
                placeholder={t('profile.emailCodePlaceholder')}
                data-testid="profile-email-code"
              />
              <div className="field-hint">
                {codeSent ? t('profile.codeSent') : t('profile.emailCodeHint')}
              </div>
            </div>
          )}
          <div className="form-field full">
            <label htmlFor="profile-display-name">{t('profile.displayName')}</label>
            <input
              id="profile-display-name"
              value={displayName}
              maxLength={128}
              placeholder={t('profile.displayNamePlaceholder')}
              onChange={(e) => setDisplayName(e.target.value)}
              data-testid="profile-display-name"
            />
          </div>
        </div>
        <div className="dialog-actions">
          <button data-testid="profile-save" disabled={saving} onClick={() => void save()}>
            {saving ? t('common.working') : t('profile.save')}
          </button>
        </div>
      </div>
    </div>
  );
}
