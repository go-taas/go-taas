// DismissDialog renders the dismiss-reason dialog (feature #50, design
// D8): a reason radio group (not_applicable | planned | other) and a
// note textarea with a 256-character counter; "other" requires a note
// (13203 otherwise).

import { useState } from 'react';
import { Dialog } from '../../components';
import { useI18n } from '../../i18n';

export const DISMISS_REASONS = ['not_applicable', 'planned', 'other'] as const;
export type DismissReason = (typeof DISMISS_REASONS)[number];
const NOTE_MAX = 256;

export default function DismissDialog({
  targetDisplay,
  onCancel,
  onSubmit,
}: {
  targetDisplay: string;
  onCancel: () => void;
  onSubmit: (reason: DismissReason, note: string) => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState<DismissReason>('not_applicable');
  const [note, setNote] = useState('');
  const noteRequired = reason === 'other';
  const canSubmit = !noteRequired || note.trim().length > 0;

  return (
    <Dialog title={t('optimization.dismissTitle')} onClose={onCancel} testId="optimization-dismiss-dialog">
      <p className="muted">{targetDisplay}</p>
      {DISMISS_REASONS.map((r) => (
        <label key={r} className="radio-row">
          <input
            type="radio"
            name="dismiss-reason"
            value={r}
            checked={reason === r}
            onChange={() => setReason(r)}
            data-testid={`optimization-dismiss-reason-${r}`}
          />
          {t(`optimization.dismissReason.${r}`)}
        </label>
      ))}
      <label>
        {t('optimization.dismissNote')}
        <textarea
          data-testid="optimization-dismiss-note"
          maxLength={NOTE_MAX}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          rows={3}
        />
        <span className="muted">
          {note.length}/{NOTE_MAX}
          {noteRequired && ` · ${t('optimization.noteRequired')}`}
        </span>
      </label>
      <div className="filter-bar">
        <button className="secondary" onClick={onCancel}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="optimization-dismiss-submit"
          disabled={!canSubmit}
          onClick={() => onSubmit(reason, note.trim())}
        >
          {t('optimization.dismissConfirm')}
        </button>
      </div>
    </Dialog>
  );
}
