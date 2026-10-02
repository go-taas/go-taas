// Console surface separation (feature-17): two route trees and two shells
// in one bundle. The surface router evaluates the moved-admin-route
// redirects first (AD7), then picks the user or admin surface by path.

import { useEffect } from 'react';
import { Router, Routes, Route, replace, useRoute } from './router';
import { OrgProvider } from './org';
import { SurfaceProvider } from './surface';
import { I18nProvider } from './i18n';
import { isAdminPath, MOVED_ADMIN_ROUTES, realmHome } from './surface-routes';
import { UserShell } from './shells/UserShell';
import { AdminShell } from './shells/AdminShell';
import UserLoginPage from './pages/user/UserLoginPage';
import UserCustomLoginPage from './pages/user/UserCustomLoginPage';
import AdminCustomLoginPage from './pages/AdminCustomLoginPage';
import UserQuickstartPage from './pages/user/QuickstartPage';
import ApiDocsPage from './pages/ApiDocsPage';
import UserUsagePage from './pages/user/UsagePage';
import UserApiKeysPage from './pages/user/ApiKeysPage';
import UserRequestLogsPage from './pages/user/RequestLogsPage';
import UserPlaygroundPage from './pages/user/PlaygroundPage';
import UserPlaygroundComparePage from './pages/user/PlaygroundComparePage';
import UserBillsPage from './pages/user/BillsPage';
import UserBillingReportsPage from './pages/user/UserBillingReportsPage';
import UserModelsPage from './pages/user/ModelsPage';
import UserModelDetailPage from './pages/user/ModelDetailPage';
import AdminLoginPage from './pages/AdminLoginPage';
import OrganizationsPage from './pages/OrganizationsPage';
import ProjectsPage from './pages/ProjectsPage';
import MembersPage from './pages/MembersPage';
import InvitationsPage from './pages/InvitationsPage';
import SSOProvidersPage from './pages/SSOProvidersPage';
import IdentityBindingsPage from './pages/IdentityBindingsPage';
import ModelsPage from './pages/ModelsPage';
import ModelDetailPage from './pages/ModelDetailPage';
import ModelVersionsPage from './pages/ModelVersionsPage';
import InferenceServicesPage from './pages/InferenceServicesPage';
import ServiceDetailPage from './pages/ServiceDetailPage';
import ClustersPage from './pages/ClustersPage';
import ClusterDetailPage from './pages/ClusterDetailPage';
import ServiceLogsPage from './pages/ServiceLogsPage';
import ServiceMetricsPage from './pages/ServiceMetricsPage';
import DeploymentHistoryPage from './pages/DeploymentHistoryPage';
import ImagesPage from './pages/ImagesPage';
import ImageDetailPage from './pages/ImageDetailPage';
import UsagePage from './pages/UsagePage';
import PricingPage from './pages/PricingPage';
import BillsPage from './pages/BillsPage';
import AccountsPage from './pages/AccountsPage';
import BillingPaymentsPage from './pages/BillingPaymentsPage';
import BillingInvoicesPage from './pages/BillingInvoicesPage';
import BillingReportsPage from './pages/BillingReportsPage';
import AuditLogsPage from './pages/AuditLogsPage';
import AutoscalingPage from './pages/AutoscalingPage';
import AcceleratorsPage from './pages/AcceleratorsPage';
import AcceleratorNodeDetailPage from './pages/AcceleratorNodeDetailPage';
import CompatibilityPage from './pages/CompatibilityPage';
import LoadTestsPage from './pages/LoadTestsPage';
import LoadTestDetailPage from './pages/LoadTestDetailPage';
import WebhooksPage from './pages/WebhooksPage';
import WebhookDetailPage from './pages/WebhookDetailPage';
import AdminNotificationsPage from './pages/AdminNotificationsPage';
import UserWebhooksPage from './pages/user/UserWebhooksPage';
import UserWebhookDetailPage from './pages/user/UserWebhookDetailPage';
import UserNotificationsPage from './pages/user/UserNotificationsPage';
import UserModelObservabilityPage from './pages/user/UserModelObservabilityPage';
import ActivityPage from './pages/user/ActivityPage';
import ObservabilityPage from './pages/ObservabilityPage';
import ModelObservabilityPage from './pages/ModelObservabilityPage';
import TracesPage from './pages/TracesPage';
import TraceDetailPage from './pages/TraceDetailPage';
import UserTracesPage from './pages/user/UserTracesPage';
import UserTraceDetailPage from './pages/user/UserTraceDetailPage';
import UserUsageKeysPage from './pages/user/UserUsageKeysPage';
import UserUsageKeyDetailPage from './pages/user/UserUsageKeyDetailPage';
import UsageKeysPage from './pages/UsageKeysPage';
import UsageKeyDetailPage from './pages/UsageKeyDetailPage';
import CostAnalyticsPage from './pages/CostAnalyticsPage';
import CostDetailPage from './pages/CostDetailPage';
import ForecastPage from './pages/ForecastPage';
import SystemStatusPage from './pages/SystemStatusPage';
import ErrorAnalysisPage from './pages/ErrorAnalysisPage';
import ErrorDetailPage from './pages/ErrorDetailPage';
import UserErrorAnalysisPage from './pages/user/UserErrorAnalysisPage';
import UserErrorDetailPage from './pages/user/UserErrorDetailPage';
import UserCostAnalyticsPage from './pages/user/UserCostAnalyticsPage';
import UserCostDetailPage from './pages/user/UserCostDetailPage';
import UserForecastPage from './pages/user/UserForecastPage';
import DataExportPage from './pages/DataExportPage';
import NotFoundPage from './pages/NotFoundPage';

export default function App() {
  return (
    <I18nProvider>
      <Router>
        <SurfaceRouter />
      </Router>
    </I18nProvider>
  );
}

function SurfaceRouter() {
  const path = useRoute();
  const moved = MOVED_ADMIN_ROUTES[path];
  if (moved) return <Redirect to={moved} />;
  if (path === '/') return <Redirect to={realmHome('user')} />;
  if (path === '/admin') return <Redirect to={realmHome('admin')} />;
  return isAdminPath(path) ? <AdminSurface path={path} /> : <UserSurface path={path} />;
}

function Redirect({ to }: { to: string }) {
  useEffect(() => {
    replace(to);
  }, [to]);
  return null;
}

function UserSurface({ path }: { path: string }) {
  return (
    <SurfaceProvider realm="user">
      <OrgProvider>
        {path === '/login' ? (
          <UserLoginPage />
        ) : path.startsWith('/login/') ? (
          <UserCustomLoginPage providerId={path.slice('/login/'.length)} />
        ) : (
          <UserShell>
            <Routes>
              <Route path="/quickstart" element={<UserQuickstartPage />} />
              <Route path="/docs" element={<ApiDocsPage />} />
              <Route path="/usage" element={<UserUsagePage />} />
              <Route path="/api-keys" element={<UserApiKeysPage />} />
              <Route path="/request-logs" element={<UserRequestLogsPage />} />
              <Route path="/playground" element={<UserPlaygroundPage />} />
              <Route path="/playground/compare" element={<UserPlaygroundComparePage />} />
              <Route path="/billing" element={<UserBillsPage />} />
              <Route path="/billing/reports" element={<UserBillingReportsPage />} />
              <Route path="/activity" element={<ActivityPage />} />
              <Route path="/webhooks" element={<UserWebhooksPage />} />
              <Route path="/webhooks/:webhookId" element={<UserWebhookDetailPage />} />
              <Route path="/notifications" element={<UserNotificationsPage />} />
              <Route path="/models" element={<UserModelsPage />} />
              <Route path="/models/:id" element={<UserModelDetailPage />} />
              <Route path="/models/:id/observability" element={<UserModelObservabilityPage />} />
              <Route path="/traces" element={<UserTracesPage />} />
              <Route path="/traces/:traceId" element={<UserTraceDetailPage />} />
              <Route path="/usage/keys" element={<UserUsageKeysPage />} />
              <Route path="/usage/keys/:apiKeyId" element={<UserUsageKeyDetailPage />} />
              <Route path="/cost" element={<UserCostAnalyticsPage />} />
              <Route path="/cost/:dimension/:value" element={<UserCostDetailPage />} />
              <Route path="/forecast" element={<UserForecastPage />} />
              <Route path="/errors" element={<UserErrorAnalysisPage />} />
              <Route path="/errors/:errorCode" element={<UserErrorDetailPage />} />
              <Route path="/account/export" element={<DataExportPage />} />
              <Route path="*" element={<NotFoundPage homePath="/usage" homeLabel="Usage" />} />
            </Routes>
          </UserShell>
        )}
      </OrgProvider>
    </SurfaceProvider>
  );
}

function AdminSurface({ path }: { path: string }) {
  return (
    <SurfaceProvider realm="admin">
      <OrgProvider>
        {path === '/admin/login' ? (
          <AdminLoginPage />
        ) : path.startsWith('/admin/login/') ? (
          <AdminCustomLoginPage providerId={path.slice('/admin/login/'.length)} />
        ) : (
          <AdminShell>
            <Routes>
              <Route path="/admin/organizations" element={<OrganizationsPage />} />
              <Route path="/admin/projects" element={<ProjectsPage />} />
              <Route path="/admin/members" element={<MembersPage />} />
              <Route path="/admin/invitations" element={<InvitationsPage />} />
              <Route path="/admin/sso" element={<SSOProvidersPage />} />
              <Route path="/admin/identity-bindings" element={<IdentityBindingsPage />} />
              <Route path="/admin/models" element={<ModelsPage />} />
              <Route path="/admin/models/:id" element={<ModelDetailPage />} />
              <Route path="/admin/models/:id/versions" element={<ModelVersionsPage />} />
              <Route path="/admin/images" element={<ImagesPage />} />
              <Route path="/admin/images/:id" element={<ImageDetailPage />} />
              <Route path="/admin/inference-services" element={<InferenceServicesPage />} />
              <Route path="/admin/clusters" element={<ClustersPage />} />
              <Route path="/admin/clusters/:clusterId" element={<ClusterDetailPage />} />
              <Route path="/admin/inference-services/:id" element={<ServiceDetailPage />} />
              <Route path="/admin/services/:id/logs" element={<ServiceLogsPage />} />
              <Route path="/admin/services/:id/metrics" element={<ServiceMetricsPage />} />
              <Route path="/admin/deployments" element={<DeploymentHistoryPage />} />
              <Route path="/admin/usage" element={<UsagePage />} />
              <Route path="/admin/pricing" element={<PricingPage />} />
              <Route path="/admin/billing" element={<BillsPage />} />
              <Route path="/admin/billing/accounts" element={<AccountsPage />} />
              <Route path="/admin/billing/payments" element={<BillingPaymentsPage />} />
              <Route path="/admin/billing/invoices" element={<BillingInvoicesPage />} />
              <Route path="/admin/billing/reports" element={<BillingReportsPage />} />
              <Route path="/admin/audit-logs" element={<AuditLogsPage />} />
              <Route path="/admin/autoscaling" element={<AutoscalingPage />} />
              <Route path="/admin/accelerators" element={<AcceleratorsPage />} />
              <Route path="/admin/accelerators/:nodeId" element={<AcceleratorNodeDetailPage />} />
              <Route path="/admin/compatibility" element={<CompatibilityPage />} />
              <Route path="/admin/load-tests" element={<LoadTestsPage />} />
              <Route path="/admin/load-tests/:loadTestId" element={<LoadTestDetailPage />} />
              <Route path="/admin/webhooks" element={<WebhooksPage />} />
              <Route path="/admin/webhooks/:webhookId" element={<WebhookDetailPage />} />
              <Route path="/admin/notifications" element={<AdminNotificationsPage />} />
              <Route path="/admin/observability" element={<ObservabilityPage />} />
              <Route path="/admin/observability/models/:modelId" element={<ModelObservabilityPage />} />
              <Route path="/admin/traces" element={<TracesPage />} />
              <Route path="/admin/traces/:traceId" element={<TraceDetailPage />} />
              <Route path="/admin/usage/keys" element={<UsageKeysPage />} />
              <Route path="/admin/usage/keys/:apiKeyId" element={<UsageKeyDetailPage />} />
              <Route path="/admin/cost" element={<CostAnalyticsPage />} />
              <Route path="/admin/cost/:dimension/:value" element={<CostDetailPage />} />
              <Route path="/admin/forecast" element={<ForecastPage />} />
              <Route path="/admin/status" element={<SystemStatusPage />} />
              <Route path="/admin/errors" element={<ErrorAnalysisPage />} />
              <Route path="/admin/errors/:errorCode" element={<ErrorDetailPage />} />
              <Route path="*" element={<NotFoundPage homePath="/admin/models" homeLabel="Models" />} />
            </Routes>
          </AdminShell>
        )}
      </OrgProvider>
    </SurfaceProvider>
  );
}
