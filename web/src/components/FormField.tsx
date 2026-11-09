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
      title={t(`deploy.reqHint.${kind}`)}
      data-testid={`deploy-req-${kind}`}
    >
      {label ?? t(`deploy.req.${kind}`)}
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
