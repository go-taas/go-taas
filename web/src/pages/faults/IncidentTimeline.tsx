// IncidentTimeline: the vertical diagnosis timeline (feature #53,
// AD11). Each step shows its timestamp, actor and detail; a failed step
// is rendered in red.

import { formatTime, type IncidentStep } from '../../api';

export default function IncidentTimeline({ steps }: { steps: IncidentStep[] }) {
  if (!steps || steps.length === 0) {
    return <div className="muted">No diagnosis steps yet.</div>;
  }
  return (
    <div className="incident-timeline" data-testid="fault-timeline">
      {steps.map((s, i) => (
        <div
          key={i}
          className={`timeline-step ${s.stepType === 'recovery_failed' ? 'error' : ''}`}
          data-testid={`fault-step-${i}`}
          style={{ display: 'flex', gap: 12, marginBottom: 8 }}
        >
          <div className="muted mono" style={{ minWidth: 120 }}>
            {formatTime(s.createdAt)}
          </div>
          <div>
            <span className="badge">{s.stepType}</span>
            <span className="muted" style={{ marginLeft: 8 }}>
              {s.actor}
            </span>
            <div>{s.detail}</div>
          </div>
        </div>
      ))}
    </div>
  );
}