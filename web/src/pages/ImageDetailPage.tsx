// Image detail page: image metadata, in-use inference services and the
// warmup task history with per-node results.
// Implements docs/design/image-management.md FR2, FR4, AC13, AC14.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  formatTime,
  type ImageSummary,
  type InUseService,
  type PageMeta,
  type WarmupTaskSummary,
} from '../api';
import { useOrg } from '../org';
import { BackLink, ErrorBanner, Pagination, StateBadge } from '../components';
import { usePolling } from '../components';

interface ImageResponse {
  response: { code: number; message: string };
  image: ImageSummary;
  inUseServices: InUseService[];
}

interface TasksResponse {
  response: { code: number; message: string };
  tasks: WarmupTaskSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function ImageDetailPage() {
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [image, setImage] = useState<ImageResponse | null>(null);
  const [tasks, setTasks] = useState<WarmupTaskSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [img, taskList] = await Promise.all([
        api.get<ImageResponse>(`/api/v1/admin/images/${id}`, orgId),
        api.get<TasksResponse>(
          `/api/v1/admin/images/${id}/warmup-tasks?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
          orgId,
        ),
      ]);
      setImage(img);
      setTasks(taskList.tasks || []);
      setTotal(parseInt(taskList.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load image');
    } finally {
      setLoading(false);
    }
  }, [id, orgId, offset]);

  useEffect(() => {
    void load();
  }, [load]);

  // Warmup tasks transition pending → running → succeeded/failed; a
  // light periodic refresh follows them without manual reloads.
  usePolling(() => void load(), 10000, !loading && !error);

  if (loading) return <div className="loading">Loading…</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/images" label="Back to Images" />
        <ErrorBanner message={error} />
      </div>
    );
  if (!image) return null;

  return (
    <div>
      <BackLink to="/admin/images" label="Back to Images" />
      <div className="page-header">
        <div>
          <h1 data-testid="image-detail-name">
            <span className="mono">{image.image.name}</span>
            <span className="mono muted">:{image.image.tag}</span>
          </h1>
          <div className="subtitle mono">{image.image.imageId}</div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>Metadata</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">Accelerator</div>
            <div className="value">{image.image.accelerator}</div>
          </div>
          <div className="detail-item">
            <div className="label">Engine</div>
            <div className="value mono">{image.image.engine}</div>
          </div>
          <div className="detail-item">
            <div className="label">In-use services</div>
            <div className="value" data-testid="image-detail-in-use-count">
              {image.inUseServices.length}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Last warmup</div>
            <div className="value">
              {image.image.lastWarmupState ? (
                <StateBadge state={image.image.lastWarmupState} />
              ) : (
                '—'
              )}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Created</div>
            <div className="value">{formatTime(image.image.createdAt)}</div>
          </div>
          {image.image.description && (
            <div className="detail-item">
              <div className="label">Description</div>
              <div className="value">{image.image.description}</div>
            </div>
          )}
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>In-use services</h3>
        {image.inUseServices.length === 0 ? (
          <div className="empty-state" data-testid="image-detail-no-in-use">
            No inference services reference this image.
          </div>
        ) : (
          <table className="data" data-testid="image-in-use-table">
            <thead>
              <tr>
                <th>Service</th>
                <th>State</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {image.inUseServices.map((svc) => (
                <tr key={svc.serviceId} data-testid={`image-in-use-row-${svc.name}`}>
                  <td>
                    <strong>{svc.name}</strong>
                  </td>
                  <td>
                    <StateBadge state={svc.state} />
                  </td>
                  <td>
                    <button
                      className="link"
                      onClick={() => navigate(`/admin/inference-services/${svc.serviceId}`)}
                    >
                      View
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel">
        <h3 style={{ marginTop: 0 }}>Warmup tasks</h3>
        {tasks.length === 0 ? (
          <div className="empty-state" data-testid="image-detail-no-tasks">
            No warmup tasks yet.
          </div>
        ) : (
          <table className="data" data-testid="image-warmup-tasks-table">
            <thead>
              <tr>
                <th>Task</th>
                <th>State</th>
                <th>Node results</th>
                <th>Failure reason</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((task) => (
                <tr key={task.taskId} data-testid={`warmup-task-row-${task.taskId}`}>
                  <td className="mono muted">{task.taskId.slice(0, 8)}…</td>
                  <td>
                    <StateBadge state={task.state} />
                  </td>
                  <td>
                    {(task.nodeResults || []).length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      (task.nodeResults || []).map((r) => (
                        <div key={r.node} className="mono">
                          {r.node}: {r.state}
                          {r.message ? ` (${r.message})` : ''}
                        </div>
                      ))
                    )}
                  </td>
                  <td className="muted">{task.failureReason || '—'}</td>
                  <td>{formatTime(task.updatedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination
            offset={offset}
            limit={PAGE_SIZE}
            total={total}
            onPageChange={setOffset}
          />
        )}
      </div>
    </div>
  );
}
