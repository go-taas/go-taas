// Form field primitives shared by the long create/update forms.
//
// A form with dozens of inputs is only scannable when it says, per field and
// per block, what actually has to be filled in. Every field is exactly one of
// three tiers:
//
//   required  — the request is rejected without a value. Some are pre-filled
//               (the model and version carried over from the catalog, the
//               image picked by the accelerator filter), so in the common case
//               only the service name is actually typed by hand;
//   defaulted — pre-filled with a working value: the platform default, the
//               fleet's own numbers, or a value derived from another field;
//   optional  — empty is a valid answer: it means "feature off" or "let the
//               engine use its own default".
//
// The badge states the tier next to the label, so an operator reads it at the
// same glance as the field name instead of hunting for a legend.

import type { ReactNode } from 'react';
import { useI18n } from '../i18n';

export type Requirement = 'required' | 'defaulted' | 'optional';

// ReqFlag is the badge next to a field (or a section) that states its tier.
export function ReqFlag({ kind, label }: { kind: Requirement; label?: string }) {
  const { t } = useI18n();
  return (
    <span
      className={`req-flag req-${kind}`}
      title={t(`form.reqHint.${kind}`)}
      data-testid={`req-flag-${kind}`}
    >
      {label ?? t(`form.req.${kind}`)}
    </span>
  );
}

// FieldLabel pairs a field's name with its requirement tier.
export function FieldLabel({
  htmlFor,
  text,
  requirement,
  flag,
}: {
  htmlFor?: string;
  text: string;
  requirement: Requirement;
  flag?: string;
}) {
  return (
    <label htmlFor={htmlFor} className="field-label">
      <span className="field-label-text">{text}</span>
      <ReqFlag kind={requirement} label={flag} />
    </label>
  );
}

// FormLegend explains the three tiers once, at the top of a form, so the badge
// next to every field is already understood when it first appears.
export function FormLegend({ testId = 'form-legend' }: { testId?: string }) {
  const { t } = useI18n();
  return (
    <div className="form-legend" data-testid={testId}>
      <span className="form-legend-item">
        <ReqFlag kind="required" />
        {t('form.reqHint.required')}
      </span>
      <span className="form-legend-item">
        <ReqFlag kind="defaulted" />
        {t('form.reqHint.defaulted')}
      </span>
      <span className="form-legend-item">
        <ReqFlag kind="optional" />
        {t('form.reqHint.optional')}
      </span>
    </div>
  );
}

// RequiredStatus is the line in a form's action row that names what is still
// missing. A value the operator has not filled in yet is then visible before
// the round trip rather than as a server error afterwards.
export function RequiredStatus({ missing }: { missing: string[] }) {
  const { t } = useI18n();
  return (
    <div
      className={`deploy-checklist ${missing.length === 0 ? 'ready' : 'pending'}`}
      data-testid="form-required-status"
    >
      {missing.length === 0
        ? t('form.requiredComplete')
        : t('form.requiredMissing', { fields: missing.join(' · ') })}
    </div>
  );
}

// FormSection is one visual block of a long form: a numbered heading, a
// one-line summary of what the block decides, the block's own tier, and a
// hairline rule separating it from the previous one. Numbering matters on a
// form this long: it gives the operator a position and a sense of progress.
export function FormSection({
  step,
  title,
  hint,
  requirement,
  children,
  testId,
}: {
  step: number;
  title: string;
  hint?: string;
  requirement?: Requirement;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <section className="form-section" data-testid={testId}>
      <div className="form-section-head">
        <span className="form-section-step" aria-hidden="true">
          {step}
        </span>
        <h4>{title}</h4>
        {requirement && <ReqFlag kind={requirement} />}
        {hint && <span className="form-section-hint">{hint}</span>}
      </div>
      {children}
    </section>
  );
}
